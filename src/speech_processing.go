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

const responseSchema = `{
	"type": "object",
	"description": "Map of activity ID to cleaned speech text",
	"additionalProperties": {
		"type": "string",
		"description": "The cleaned speech text for this activity"
	}
}`

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

	// Parse the JSON response
	var result map[string]string
	if err := json.Unmarshal([]byte(resp.Text()), &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Convert string keys to int keys
	resultMap := make(map[int]string)
	for key, value := range result {
		var activityID int
		if _, err := fmt.Sscanf(key, "%d", &activityID); err != nil {
			logger.Warn(fmt.Sprintf("Failed to parse activity ID '%s': %v", key, err))
			continue
		}
		resultMap[activityID] = value
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
