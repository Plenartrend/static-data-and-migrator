package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/agnivade/levenshtein"
	"github.com/jmoiron/sqlx"
	"google.golang.org/genai"
)

var geminiClient *genai.Client = nil // lazy initialized

// TODO: Initialize once on main startup
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

/*var responseSchema = genai.Schema{
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
} */

// TODO: Merge overlapping speeches
func getRelevantPartsOfSpeechForSpeaker(speakerName string, protocol *Protocol) ([]string, error) {
	if protocol == nil {
		return nil, fmt.Errorf("protocol cannot be nil")
	}
	text := protocol.Text

	//namePattern := `([A-ZÄÖÜ][a-zäöüß]+(?:[\s\n]+[A-ZÄÖÜ][a-zäöüß]+)*)[\s\n]+([A-ZÄÖÜ][a-zäöüß]+)(?:[\s\n]*)\((.{1,30})\):`
	namePattern := regexp.QuoteMeta(speakerName) + ":.{0,1000}"
	re := regexp.MustCompile(namePattern)

	matches := re.FindAllStringIndex(text, -1)

	if len(matches) == 0 {
		return nil, nil // No matches
	}

	result := []string{}
	for _, match := range matches {
		endIndex := min(match[1]+11000, len(text))
		result = append(result, text[match[0]:endIndex])
	}

	return result, nil
}

// TODO: Get Firstname, Lastname, Groupname. Actually we need group shortname, but it seems so far we only safe long name?
func assignSpeechesToActivities() error {
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	consoleLogLevel := Debug
	databaseLogLevel := Debug
	logger := NewLogger(db, &consoleLogLevel, &databaseLogLevel)

	var protocols []Protocol
	//err = db.Select(&protocols, "SELECT * FROM protocols p WHERE EXISTS (SELECT 1 FROM activities a WHERE a.protocol_id = p.id AND a.text IS NULL OR a.text = '')")
	err = db.Select(&protocols, "SELECT * FROM protocols p WHERE p.ID = 5626 AND EXISTS (SELECT 1 FROM activities a WHERE a.protocol_id = p.id AND a.text IS NULL OR a.text = '')")
	if err != nil {
		logger.Error(fmt.Sprintf("failed to select protocols: %v", err))
		return fmt.Errorf("failed to select protocols: %w", err)
	}

	for _, protocol := range protocols {
		var activities []Activity
		//err = db.Select(&activities, "SELECT * FROM activities a WHERE protocol_id = $1 AND a.type = 'Rede' AND (text IS NULL OR text = '')", protocol.ID)
		err = db.Select(&activities, "SELECT * FROM activities a WHERE protocol_id = $1 AND a.type = 'Rede' AND (text IS NULL OR text = '') LIMIT 50", protocol.ID)
		logger.Debug(fmt.Sprintf("Found %d activities for protocol %d", len(activities), protocol.ID))
		if err != nil {
			logger.Error(fmt.Sprintf("failed to select activities: %v", err))
			return fmt.Errorf("failed to select activities: %w", err)
		}
		var activitiesGroupedBySpeaker = make(map[string][]Activity)
		for _, activity := range activities {
			var role Role
			err = db.Get(&role, "SELECT * FROM roles WHERE id = $1", activity.RoleID)
			if err != nil {
				logger.Error(fmt.Sprintf("failed to select role: %v", err))
				return fmt.Errorf("failed to select role: %w", err)
			}
			if !role.GroupID.Valid {
				logger.Warn(fmt.Sprintf("skipping activity %d: role %d has no group", activity.ID, activity.RoleID))
				continue
			}
			var groupName string
			err = db.Get(&groupName, "SELECT name FROM parliamentary_groups WHERE id = $1", role.GroupID)
			if err != nil {
				logger.Error(fmt.Sprintf("failed to select group name: %v", err))
				return fmt.Errorf("failed to select group name: %w", err)
			}
			var speakerName string = role.FirstName + " " + role.LastName + " (" + groupName + ")"
			activitiesGroupedBySpeaker[speakerName] = append(activitiesGroupedBySpeaker[speakerName], activity)
		}

		for speaker, activities := range activitiesGroupedBySpeaker {
			activityIDs := []int{}
			for _, activity := range activities {
				activityIDs = append(activityIDs, activity.ID)
			}
			speeches, err := getRelevantPartsOfSpeechForSpeaker(speaker, &protocol)
			if err != nil {
				logger.Error(fmt.Sprintf("failed to get speeches for activities: %v", err))
				return fmt.Errorf("failed to get speeches for activities: %w", err)
			}
			activitiesTextsChan <- ActivitiesTexts{
				Activities: activityIDs,
				Texts:      speeches,
				Speaker:    speaker,
				Protocol:   &protocol,
			}
		}
	}
	return nil
}

func testAiClient() {
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
				Description: "The first 3-4 sentences of the speech IN EXACTLY THE ORIGINAL FORMATTING INCLUDING ANY ARTIFACTS LIKE NBSP AND NEWLINES instead of regular spaces. If its very generic, return a bit more.",
			},
			"speech_text_end": {
				Type:        "string",
				Description: "The last 3-4 sentences of the speech IN EXACTLY THE ORIGINAL FORMATTING INCLUDING ANY ARTIFACTS LIKE NBSP AND NEWLINES instead of regular spaces. If its very generic, return a bit more.",
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

// findCandidatePositionsByWords searches for the needle word-by-word to find promising positions
func findCandidatePositionsByWords(needle string, haystack string, logger *Logger) []int {
	// Split needle into words
	words := strings.Fields(needle)
	if len(words) < 3 {
		// Too few words, fall back to all word boundaries
		return nil
	}

	// Search for each word and track positions
	wordPositions := make([][]int, len(words))
	for i, word := range words {
		pos := 0
		for {
			idx := strings.Index(haystack[pos:], word)
			if idx == -1 {
				break
			}
			wordPositions[i] = append(wordPositions[i], pos+idx)
			pos += idx + len(word)
		}
	}

	// Find sequences where words appear in order (with some flexibility)
	candidates := make(map[int]bool)
	maxWordGap := 200 // Max characters between consecutive words

	// Start with positions of the first word
	for _, firstPos := range wordPositions[0] {
		currentPos := firstPos
		matchedWords := 1

		// Try to find subsequent words
		for wordIdx := 1; wordIdx < len(words); wordIdx++ {
			found := false
			for _, pos := range wordPositions[wordIdx] {
				if pos > currentPos && pos-currentPos < maxWordGap {
					currentPos = pos
					matchedWords++
					found = true
					break
				}
			}
			if !found {
				break
			}
		}

		// If we matched most words, this is a candidate position
		if matchedWords >= len(words)*2/3 { // At least 2/3 of words matched
			candidates[firstPos] = true
		}
	}

	// Convert to slice
	result := make([]int, 0, len(candidates))
	for pos := range candidates {
		result = append(result, pos)
	}

	return result
}

// findBestMatch uses Levenshtein distance to find the best matching substring
// Optimized with word-by-word exact search first, then fuzzy matching only at candidate positions
func findBestMatch(needle string, haystack string, maxDistanceRatio float64, logger *Logger) (startIdx int, found bool) {
	startTime := time.Now()
	needleLen := len(needle)
	haystackLen := len(haystack)

	logger.Debug(fmt.Sprintf("findBestMatch called: needleLen=%d, haystackLen=%d, maxDistanceRatio=%.2f", needleLen, haystackLen, maxDistanceRatio))

	if needleLen == 0 || needleLen > haystackLen {
		logger.Debug(fmt.Sprintf("findBestMatch: invalid input (needleLen=%d, haystackLen=%d)", needleLen, haystackLen))
		return -1, false
	}

	maxDistance := int(float64(needleLen) * maxDistanceRatio)
	bestDistance := maxDistance + 1
	bestIdx := -1

	// First, try word-by-word exact search to find candidate positions
	candidatePositions := findCandidatePositionsByWords(needle, haystack, logger)

	if len(candidatePositions) == 0 {
		// Try progressively removing words from the beginning, but never more than 20% of the words
		logger.Debug("Word-by-word search found no candidates, trying with words removed from beginning")
		words := strings.Fields(needle)

		if len(words) > 3 {
			maxRemove := int(float64(len(words)) * 0.50)
			if maxRemove < 1 {
				maxRemove = 1
			}
			if maxRemove > len(words)-3 {
				maxRemove = len(words) - 3
			}

			for wordsToRemove := 1; wordsToRemove <= maxRemove; wordsToRemove++ {
				shortenedNeedle := strings.Join(words[wordsToRemove:], " ")
				candidatePositions = findCandidatePositionsByWords(shortenedNeedle, haystack, logger)

				if len(candidatePositions) > 0 {
					logger.Info(fmt.Sprintf("Found candidates after removing %d words from beginning", wordsToRemove))
					break
				}
			}
		}

		if len(candidatePositions) == 0 {
			duration := time.Since(startTime)
			logger.Warn(fmt.Sprintf("findBestMatch: no candidates found even after progressive word removal in %v - skipping", duration))
			return -1, false
		}
	} else {
		logger.Debug(fmt.Sprintf("Word-by-word search found %d promising positions", len(candidatePositions)))
	}

	logger.Debug(fmt.Sprintf("findBestMatch: checking %d candidate positions", len(candidatePositions)))

	// Fast path: if there is only one candidate, try exact match without fuzzy search
	if len(candidatePositions) == 1 {
		pos := candidatePositions[0]
		if pos+needleLen <= haystackLen {
			window := haystack[pos : pos+needleLen]
			distance := levenshtein.ComputeDistance(needle, window)
			maxAllowed := int(float64(needleLen) * 0.25) // require at least 75% similarity
			if distance <= maxAllowed {
				duration := time.Since(startTime)
				logger.Debug(fmt.Sprintf("findBestMatch: single candidate accepted at index %d with distance %d (maxAllowed=%d) in %v", pos, distance, maxAllowed, duration))
				return pos, true
			}
			// Distance too high
			duration := time.Since(startTime)
			logger.Warn(fmt.Sprintf("findBestMatch: single candidate distance %d exceeds maxAllowed %d (skipping) in %v", distance, maxAllowed, duration))
			return -1, false
		}
		// Candidate beyond haystack bounds
		duration := time.Since(startTime)
		logger.Warn(fmt.Sprintf("findBestMatch: single candidate out of bounds (pos=%d, needleLen=%d, haystackLen=%d) in %v", pos, needleLen, haystackLen, duration))
		return -1, false
	}

	// Try different window sizes around the needle length to account for small variations
	for windowSize := needleLen - maxDistance; windowSize <= needleLen+maxDistance; windowSize++ {
		if windowSize <= 0 || windowSize > haystackLen {
			continue
		}

		// Only check candidate positions
		for _, pos := range candidatePositions {
			if pos+windowSize > haystackLen {
				continue
			}

			window := haystack[pos : pos+windowSize]
			distance := levenshtein.ComputeDistance(needle, window)

			if distance < bestDistance {
				bestDistance = distance
				bestIdx = pos
			}

			// Early exit if we find an exact match
			if distance == 0 {
				duration := time.Since(startTime)
				logger.Debug(fmt.Sprintf("findBestMatch: exact match found at index %d in %v", bestIdx, duration))
				return bestIdx, true
			}
		}
	}

	duration := time.Since(startTime)
	if bestIdx >= 0 && bestDistance <= maxDistance {
		logger.Debug(fmt.Sprintf("findBestMatch: fuzzy match found at index %d with distance %d in %v", bestIdx, bestDistance, duration))
		return bestIdx, true
	}

	logger.Debug(fmt.Sprintf("findBestMatch: no match found (bestDistance=%d > maxDistance=%d) in %v", bestDistance, maxDistance, duration))
	return -1, false
}

// getSpeechByStartAndEnd uses fuzzy matching to find speech boundaries
// Searches in the full protocol text (optimized with word-by-word search, so only a few candidates are checked)
func getSpeechByStartAndEnd(firstSentences string, lastSentences string, protocol *Protocol, logger *Logger) (string, error) {
	logger.Debug(fmt.Sprintf("getSpeechByStartAndEnd called with firstSentences (len=%d), lastSentences (len=%d)", len(firstSentences), len(lastSentences)))
	if protocol == nil {
		return "", fmt.Errorf("protocol cannot be nil")
	}

	text := protocol.Text

	// Try exact match first
	startIdx := strings.Index(text, firstSentences)
	if startIdx == -1 {
		// Fall back to fuzzy matching with 20% tolerance
		logger.Debug("Exact match failed for start, trying fuzzy match")
		var found bool
		startIdx, found = findBestMatch(firstSentences, text, 0.2, logger)
		if !found {
			// Log detailed information for debugging - this is expected to happen sometimes
			protocolPreview := text
			if len(protocolPreview) > 1000 {
				protocolPreview = protocolPreview[:1000] + "..."
			}
			logger.Warn(fmt.Sprintf("No match found for start (skipping speech). Start text:\n%s\nEnd text:\n%s\nProtocol (first 1000 chars):\n%s", firstSentences, lastSentences, protocolPreview))
			return "", fmt.Errorf("could not find start of speech - skipping")
		}
		logger.Info(fmt.Sprintf("Found fuzzy match for start at index %d", startIdx))
	}

	// Search for end only after the start
	endSearchText := text[startIdx:]
	endIdx := strings.LastIndex(endSearchText, lastSentences)

	if endIdx == -1 {
		// Fall back to fuzzy matching for end
		logger.Debug("Exact match failed for end, trying fuzzy match")
		var found bool
		endIdx, found = findBestMatch(lastSentences, endSearchText, 0.2, logger)
		if !found {
			// Log detailed information for debugging - this is expected to happen sometimes
			protocolPreview := endSearchText
			if len(protocolPreview) > 1000 {
				protocolPreview = protocolPreview[:1000] + "..."
			}
			logger.Warn(fmt.Sprintf("No match found for end (skipping speech). Start text:\n%s\nEnd text:\n%s\nProtocol (remaining text, first 1000 chars):\n%s", firstSentences, lastSentences, protocolPreview))
			return "", fmt.Errorf("could not find end of speech - skipping")
		}
		logger.Info(fmt.Sprintf("Found fuzzy match for end at relative index %d", endIdx))
	}

	endIdx = startIdx + endIdx + len(lastSentences)

	if endIdx <= startIdx {
		return "", fmt.Errorf("invalid speech boundaries: start=%d, end=%d", startIdx, endIdx)
	}

	logger.Debug(fmt.Sprintf("Extracted speech from index %d to %d (length=%d)", startIdx, endIdx, endIdx-startIdx))
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
	temperature := float32(0)
	resp, err := client.Models.GenerateContent(context.Background(), geminiModel, []*genai.Content{
		{
			Parts: parts,
		},
	}, &genai.GenerateContentConfig{
		SystemInstruction:  systemInstruction,
		ResponseMIMEType:   "application/json",
		ResponseJsonSchema: responseSchema,
		Temperature:        &temperature,
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
