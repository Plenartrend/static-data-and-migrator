package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"google.golang.org/genai"
)

var geminiClient *genai.Client = nil // lazy initialized

func getGeminiClient() (*genai.Client, error) {
	var err error = nil
	if geminiClient == nil {
		geminiClient, err = genai.NewClient(context.Background(), &genai.ClientConfig{
			APIKey:  os.Getenv("GEMINI_API_KEY"),
			Backend: genai.BackendGeminiAPI,
		})
	}
	return geminiClient, err
}

const geminiModel string = "gemini-2.5-flash"

var responseSchema = genai.Schema{
	Type:        "array",
	Description: "Array of activity ID to cleaned speech text mappings",
	Items: &genai.Schema{
		Type: "object",
		Properties: map[string]*genai.Schema{
			"activity_id": {
				Type:        "integer",
				Description: "The ID of the activity",
			},
			"speech_text": {
				Type:        "string",
				Description: "The cleaned speech text for this activity",
			},
		},
		Required: []string{"activity_id", "speech_text"},
	},
}

const systemInstructionText string = `It is your job to remove noise from speeches and then assign them to activities. The speeches are held by politicians in the German parliament and are held in the German language. 
An Activity is like a container for a speech. An activity has an ID and a speaker. 
You will get a list of activities and a list of texts. The texts CONTAIN speeches, but not only speeches — they contain noise and parts of other speeches. 
One speech is a full speech by one person, including all questions and answers. You must extract the speech from start to finish, truncating anything like parts from other speeches at the beginning and end.
It is fine if textparts like who is asking a question or who interjects stay part of the speech.
It is possible that you get multiple speeches and multiple activities. The number of speeches must always match the number of activities. The speeches are in the same order as the IDs of the activities.
You may get parts of speeches where the politician from the Activity does not actually hold the speech, but only asks a question. This is NOT A SPEECH BY THE SPEAKER and must be removed.
Texts may be overlapping because an answer to a question may cause the texts to split. THEY ARE PART OF THE SAME SPEECH AND MUST BE MERGED.
`

func processSpeeches(speeches []string, speakerName string, activities []int, logger *Logger) (map[int]string, error) {
	logger.Debug(fmt.Sprintf("processSpeeches called with speakerName='%s', activities=%v, speeches count=%d", speakerName, activities, len(speeches)))
	for i, speech := range speeches {
		logger.Debug(fmt.Sprintf("  speech[%d] (length=%d): %s", i, len(speech), speech))
	}

	client, err := getGeminiClient()

	if err != nil {
		return nil, err
	}

	systemInstruction := &genai.Content{
		Parts: []*genai.Part{
			{Text: systemInstructionText},
		},
	}
	var parts []*genai.Part

	var activitiesPrompt genai.Part = genai.Part{
		Text: "--------These are the Activities: --------\n",
	}
	parts = append(parts, &activitiesPrompt)

	for _, activity := range activities {
		parts = append(parts, &genai.Part{
			Text: fmt.Sprintf("Activity ID: %d, Speaker: %s\n", activity, speakerName),
		})
	}

	var speechesPrompt genai.Part = genai.Part{
		Text: "--------These are the Speeches: --------\n",
	}

	parts = append(parts, &speechesPrompt)

	for _, speech := range speeches {
		parts = append(parts, &genai.Part{
			Text: speech,
		})
	}

	resp, err := client.Models.GenerateContent(context.Background(), geminiModel, []*genai.Content{
		{
			Parts: parts,
		},
	}, &genai.GenerateContentConfig{
		SystemInstruction:  systemInstruction,
		ResponseMIMEType:   "application/json",
		ResponseJsonSchema: responseSchema,
	})

	if err != nil {
		return nil, err
	}

	// Log the raw model response
	responseText := resp.Text()
	logger.Debug(fmt.Sprintf("Gemini model response (length=%d): %s", len(responseText), responseText))

	// Parse the JSON response as an array
	var rawResult []map[string]interface{}
	if err := json.Unmarshal([]byte(resp.Text()), &rawResult); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Convert to map[int]string
	resultMap := make(map[int]string)
	for _, item := range rawResult {
		activityID, ok := item["activity_id"].(float64)
		if !ok {
			logger.Warn(fmt.Sprintf("Failed to parse activity_id from item: %v", item))
			continue
		}
		speechText, ok := item["speech_text"].(string)
		if !ok {
			logger.Warn(fmt.Sprintf("Failed to parse speech_text for activity ID %d", int(activityID)))
			continue
		}
		resultMap[int(activityID)] = speechText
	}

	return resultMap, nil
}

func test() {
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  os.Getenv("GEMINI_API_KEY"),
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		fmt.Println("Failed to create Gemini client:", err)
		return
	}

	parts := []*genai.Part{
		{Text: "hello"},
	}

	resp, err := client.Models.GenerateContent(context.Background(), geminiModel, []*genai.Content{
		{Parts: parts},
	}, nil)
	if err != nil {
		fmt.Println("Error generating content:", err)
		return
	}

	fmt.Println(resp.Text())
}

//---------------Start/End------------------//

/*
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"google.golang.org/genai"
)

var geminiClient *genai.Client = nil // lazy initialized

func getGeminiClient() (*genai.Client, error) {
	var err error = nil
	if geminiClient == nil {
		geminiClient, err = genai.NewClient(context.Background(), &genai.ClientConfig{
			APIKey:  os.Getenv("GEMINI_API_KEY"),
			Backend: genai.BackendGeminiAPI,
		})
	}
	return geminiClient, err
}

const geminiModel string = "gemini-2.5-flash"

var responseSchema = genai.Schema{
	Type:        "array",
	Description: "Array of activity ID to cleaned speech text mappings",
	Items: &genai.Schema{
		Type: "object",
		Properties: map[string]*genai.Schema{
			"activity_id": {
				Type:        "integer",
				Description: "The ID of the activity",
			},
			"speech_text_start": {
				Type:        "string",
				Description: "The first 3-4 sentences of the speech IN EXACTLY THE ORIGINAL FORMATTING INCLUDING ANY ARTIFACTS LIKE NBSP instead of regular spaces. If its very generic, return a bit more.",
			},
			"speech_text_end": {
				Type:        "string",
				Description: "The last 3-4 sentences of the speech IN EXACTLY THE ORIGINAL FORMATTING INCLUDING ANY ARTIFACTS LIKE NBSP instead of regular spaces. If its very generic, return a bit more.",
			},
		},
		Required: []string{"activity_id", "speech_text_start", "speech_text_end"},
	},
}

const systemInstructionText string = `It is your job to remove noise from speeches and then assign them to activities. The speeches are held by politicians in the German parliament and are held in the German language.
An Activity is like a container for a speech. An activity has an ID and a speaker.
You will get a list of activities and a list of texts. The texts CONTAIN speeches, but not only speeches — they contain noise and parts of other speeches.
One speech is a full speech by one person, including all questions and answers. You must extract the speech from start to finish, truncating anything like parts from other speeches at the beginning and end.
It is fine if textparts like who is asking a question or who interjects stay part of the speech.
It is possible that you get multiple speeches and multiple activities. The number of speeches must always match the number of activities. The speeches are in the same order as the IDs of the activities.
You may get parts of speeches where the politician from the Activity does not actually hold the speech, but only asks a question. This is NOT A SPEECH BY THE SPEAKER and must be removed.
Texts may be overlapping because an answer to a question may cause the texts to split. THEY ARE PART OF THE SAME SPEECH AND MUST BE MERGED.
You must return the first 3-4 sentences and the last 3-4 sentences of each speech in the original formatting, so I can programatically reconstruct the full speech. This uses less tokens than you returning the full speech. Therefore you must be CAREFUL TO MATCH EXACTLY THE ORIGINAL FORMATTING.
`

// Does not work very well; too many artifacts in original texts. Maybe we should think about cleaning it up first; e.g. replacing NBSP with regular spaces.
func getSpeechByStartAndEnd(firstSentences string, lastSentences string, protocol *Protocol, logger *Logger) (string, error) {
	logger.Debug(fmt.Sprintf("getSpeechByStartAndEnd called with firstSentences='%s', lastSentences='%s'", firstSentences, lastSentences))
	if protocol == nil {
		return "", fmt.Errorf("protocol cannot be nil")
	}
	text := protocol.Text

	if strings.Count(text, firstSentences) > 1 || strings.Count(text, lastSentences) > 1 {
		return "", fmt.Errorf("first (%s) or last (%s) sentences found multiple times in protocol", firstSentences, lastSentences)
	}

	startIdx := strings.Index(text, firstSentences)
	if startIdx == -1 {
		return "", fmt.Errorf("could not find start of speech in protocol")
	}

	endSearchText := text[startIdx:]

	endRelIdx := strings.LastIndex(endSearchText, lastSentences)
	if endRelIdx == -1 {
		return "", fmt.Errorf("could not find end of speech in protocol after start")
	}
	endIdx := startIdx + endRelIdx + len(lastSentences)

	if endIdx <= startIdx {
		return "", fmt.Errorf("invalid speech boundaries")
	}

	return text[startIdx:endIdx], nil
}

func processSpeeches(speeches []string, speakerName string, activities []int, protocol *Protocol, logger *Logger) (map[int]string, error) {
	logger.Debug(fmt.Sprintf("processSpeeches called with speakerName='%s', activities=%v, speeches count=%d", speakerName, activities, len(speeches)))

	client, err := getGeminiClient()

	if err != nil {
		return nil, err
	}

	systemInstruction := &genai.Content{
		Parts: []*genai.Part{
			{Text: systemInstructionText},
		},
	}
	var parts []*genai.Part

	var activitiesPrompt genai.Part = genai.Part{
		Text: "--------These are the Activities: --------\n",
	}
	parts = append(parts, &activitiesPrompt)

	for _, activity := range activities {
		parts = append(parts, &genai.Part{
			Text: fmt.Sprintf("Activity ID: %d, Speaker: %s\n", activity, speakerName),
		})
	}

	var speechesPrompt genai.Part = genai.Part{
		Text: "--------These are the Speeches: --------\n",
	}

	parts = append(parts, &speechesPrompt)

	for _, speech := range speeches {
		parts = append(parts, &genai.Part{
			Text: speech,
		})
	}

	resp, err := client.Models.GenerateContent(context.Background(), geminiModel, []*genai.Content{
		{
			Parts: parts,
		},
	}, &genai.GenerateContentConfig{
		SystemInstruction:  systemInstruction,
		ResponseMIMEType:   "application/json",
		ResponseJsonSchema: responseSchema,
	})

	if err != nil {
		return nil, err
	}

	// Log the raw model response
	responseText := resp.Text()
	logger.Debug(fmt.Sprintf("Gemini model response (length=%d): %s", len(responseText), responseText))

	// Parse the JSON response as an array
	var rawResult []map[string]interface{}
	if err := json.Unmarshal([]byte(resp.Text()), &rawResult); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Convert to map[int]string
	resultMap := make(map[int]string)
	for _, item := range rawResult {
		activityID, ok := item["activity_id"].(float64)
		if !ok {
			logger.Warn(fmt.Sprintf("Failed to parse activity_id from item: %v", item))
			continue
		}
		speechTextStart, ok := item["speech_text_start"].(string)
		speechTextEnd, ok := item["speech_text_end"].(string)
		if !ok {
			logger.Warn(fmt.Sprintf("Failed to parse speech_text_start or speech_text_end for activity ID %d", int(activityID)))
			continue
		}
		speechText, err := getSpeechByStartAndEnd(speechTextStart, speechTextEnd, protocol, logger)
		if err != nil {
			logger.Warn(fmt.Sprintf("Failed to get speech by start and end for activity ID %d: %v", int(activityID), err))
			continue
		}
		resultMap[int(activityID)] = speechText
	}

	return resultMap, nil
}

*/
