package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"google.golang.org/genai"
)

type PreviouslyUnfinishedSpeech struct {
	Present           bool
	Speaker           string
	SpeechStart       string
	BeginningTooShort bool
}

const iterativeInstructionText = `
You will receive a chunk of a German parliamentary protocol. Your task is to extract speeches.
A speech is the full statement of one person, including any interruptions (e.g., questions or interjections by others), WHICH ALWAYS SHOULD BE INCLUDED AS PART OF THE SPEECH.

IMPORTANT: Chunks are split at hard boundaries. The previous chunk stopped at an exact character position, and this chunk starts exactly where the previous one ended. There is NO overlap. The text continues directly from where it was cut (possibly mid-word).

1. Identify speeches that start and end within this chunk. For each, return the speaker's full name, the first 15-30 words, and the last 15-30 words of the speech. If the beginning is very generic use closer to 30 words, else closer to 15.
2. If a speech starts in this chunk but does not end, return the speaker's full name and the first 15-30 words if possible.
3. If a speech started in a previous chunk and continues here, you already received the beginning of the speech. If the beginning is flagged as too short or generic,
the text was cut at an arbitrary point (possibly mid-word). This chunk continues EXACTLY from that point - DO NOT add spaces but simply concat the beginning of this chunk until the TOTAL beginning is 15 to at most 30 words.
Return the speech either in the completed section or in the "started" section if its still not complete, and use the concatinated speech_text_start as the speech_text_start.

"Speaker's full name" refers to the format "FirstName LastName" with no titles, honorifics, or party affiliations.
Speeches always begin with the speaker being named (e.g., "Dr. Angela Merkel (CDU/CSU):").
Note that questions or interjections also begin with a speaker name and should be included as part of the ongoing speech rather than separated.
Do not include "speeches" by the president or vice president of the parliament.
You must return the first 15-30 words and the last 15-30 words of each speech in the original formatting, so I can programatically reconstruct the full speech.
This uses less tokens than you returning the full speech. Therefore you must be CAREFUL TO MATCH EXACTLY THE ORIGINAL FORMATTING INCLUDING ANY ARTIFACTS LIKE NBSP AND NEWLINES.

CRITICAL RULES - EXACT SUBSTRING EXTRACTION:
- Copy text EXACTLY character by character from the original
- DO NOT use ".." (ellipsis) or "..." to skip or summarize parts
- DO NOT paraphrase, reformat, or reconstruct sentences
- DO NOT add or remove any characters, spaces, or newlines
- DO NOT CHANGE THE CASING OF ANY LETTER
- EVERY character must match the original PERFECTLY
- The rule is: AS FEW WORDS AS POSSIBLE, BUT AS MANY AS NECESSARY TO IDENTIFY THE SPEECH; NEVER MORE THAN 30 WORDS.
`

const chunkSize = 50_000

func getResponseSchema() *genai.Schema {
	return &genai.Schema{
		Type:        "object",
		Description: "Container for iterative speech extraction and assignment to activities.",
		Properties: map[string]*genai.Schema{
			"complete_speeches": {
				Type:        "array",
				Description: "List of speeches that both start and end within this chunk.",
				Items: &genai.Schema{
					Type: "object",
					Properties: map[string]*genai.Schema{
						"speaker": {
							Type:        "string",
							Description: "The full name of the speaker that gave the speech. Do NOT include titles like 'Dr.' or 'Prof.'",
						},
						"speech_text_start": {
							Type:        "string",
							Description: "EXACT substring: The first 15-30 words of the speech copied character-by-character from the original text. NO ellipsis (..), NO paraphrasing, NO changes. Must include exact formatting with NBSP, newlines, and all artifacts. Also include interruptions by others like interjections or clapping if they are in the original text.",
						},
						"speech_text_end": {
							Type:        "string",
							Description: "EXACT substring: The last 15-30 words of the speech copied character-by-character from the original text. NO ellipsis (..), NO paraphrasing, NO changes. Must include exact formatting with NBSP, newlines, and all artifacts. Also include interruptions by others like interjections or clapping if they are in the original text.",
						},
					},
				},
			},
			"started_speech": {
				Type:        "object",
				Description: "Contains the beginning part of a speech that has started in this chunk but is not yet complete.",
				Properties: map[string]*genai.Schema{
					"present": {
						Type:        "boolean",
						Description: "Indicates if there is a speech that has started but is not yet complete.",
					},
					"speaker": {
						Type:        "string",
						Description: "The full name of the speaker that gave the started speech. Do NOT include titles like 'Dr.' or 'Prof.'",
					},
					"begin_of_speech_start": {
						Type:        "string",
						Description: "EXACT substring: The first 15-30 words of the speech copied character-by-character from the original text. NO ellipsis (..), NO paraphrasing, NO changes. Must include exact formatting with NBSP, newlines, and all artifacts. Also include interruptions by others like interjections or clapping if they are in the original text.",
					},
					"beginning_too_short": {
						Type:        "boolean",
						Description: "Indicates if the beginning of the speech is too short or generic because the text was truncated. Only set to true if the beginning is very likely too generic to identify the text. ONLY do this if you are really sure that the beginning is too short, as this can lead to errors.",
					},
				},
			},
		},
	}
}

func addTextToActivity(protocolId int, speaker string, speech string, db DBInterface, logger *Logger) error {
	var activityId int
	err := db.Get(&activityId, `
			SELECT a.id
			FROM activities a, roles r
			WHERE a.role_id = r.id
				AND a.protocol_id = $1
				AND a.type = 'Rede'
				AND CONCAT(r.first_name, ' ', r.last_name) = $2
			ORDER BY a.id asc
			LIMIT 1
		`, protocolId, speaker)
	if err == sql.ErrNoRows {
		logger.Warn(fmt.Sprintf("could not correlate speech by speaker %s in protocol %d to an activity", speaker, protocolId))
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to find activity for speaker %s in protocol %d: %w", speaker, protocolId, err)
	}

	_, err = db.Exec("UPDATE activities SET text = $2 WHERE id = $1", activityId, strings.ToValidUTF8(speech, ""))
	if err != nil {
		return fmt.Errorf("failed to update activity %d with speech for speaker %s: %w", activityId, speaker, err)
	}
	return nil
}

// TODO use db transaction
func processSpeechesIterative(protocol *Protocol, db DBInterface, logger *Logger) error {
	// Reset global counter for unmatched speeches at the start of each run
	unmatchedSpeechesCount = 0

	protocolText := protocol.Text
	protocolId := protocol.ID
	client, err := getGeminiClient()

	if err != nil {
		return fmt.Errorf("failed to get Gemini client: %w", err)
	}

	systemInstruction := &genai.Content{
		Parts: []*genai.Part{
			{Text: iterativeInstructionText},
		},
	}

	var previouslyUnfinishedSpeech PreviouslyUnfinishedSpeech = PreviouslyUnfinishedSpeech{
		Present:           false,
		Speaker:           "",
		SpeechStart:       "",
		BeginningTooShort: false,
	}

	startTime := time.Now()
	updatedActivities := 0

	for i := 0; i < len(protocolText); i += chunkSize {
		end := min(i+chunkSize, len(protocolText))
		chunk := protocolText[i:end]

		logger.Debug(fmt.Sprintf("Processing chunk from index %d to %d", i, end))

		var contextText string
		if previouslyUnfinishedSpeech.Present {
			contextText = "The previous chunk contained an unfinished speech by " + previouslyUnfinishedSpeech.Speaker + ". It started with: " + previouslyUnfinishedSpeech.SpeechStart
			if previouslyUnfinishedSpeech.BeginningTooShort == true {
				contextText += ". The previous chunk stopped at a hard boundary (possibly mid-word). This chunk starts EXACTLY where the previous chunk ended. You must add a few words from the beginning of this chunk to speech_text_start until the total is 15-30 words. DO NOT ADD SPACES OR SIMILAR, APPEND IMMEDEATELY AT THE END OF speech_text_start."
			}
			contextText += "\n"
		} else {
			contextText = "The previous chunk did NOT contain an unfinished speech.\n"
		}

		content := []*genai.Content{
			{
				Parts: []*genai.Part{
					{
						Text: contextText,
					},
					{
						Text: "The protocol chunk is:\n",
					},
					{
						Text: chunk,
					},
				},
			},
		}

		resp, err := client.Models.GenerateContent(context.Background(), geminiModel, content, &genai.GenerateContentConfig{
			SystemInstruction:  systemInstruction,
			ResponseMIMEType:   "application/json",
			ResponseJsonSchema: getResponseSchema(),
		})

		if err != nil {
			return err
		}

		// Log the raw model response
		responseText := resp.Text()
		logger.Debug(fmt.Sprintf("Gemini model response (length=%d): %s", len(responseText), responseText))

		var resultMap map[string]any
		if err := json.Unmarshal([]byte(resp.Text()), &resultMap); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}

		logger.Debug(fmt.Sprintf("Parsed response: %+v", resultMap))

		// Handle complete_speeches
		completeSpeechesArray := resultMap["complete_speeches"].([]any)
		for _, speechEntry := range completeSpeechesArray {
			speechMap := speechEntry.(map[string]any)
			speaker := speechMap["speaker"].(string)
			speechTextStart := speechMap["speech_text_start"].(string)
			speechTextEnd := speechMap["speech_text_end"].(string)

			// Reconstruct the full speech using start and end (with fuzzy matching)
			// Searches in full protocol (optimized with word-by-word search, so only a few candidates are checked)
			fullSpeech, err := getSpeechByStartAndEnd(speechTextStart, speechTextEnd, protocol, logger)
			if err != nil {
				logger.Warn(fmt.Sprintf("Skipping speech for speaker %s: %v", speaker, err))
				unmatchedSpeechesCount++
				// Clear previously unfinished speech if this was supposed to complete it
				if previouslyUnfinishedSpeech.Present && previouslyUnfinishedSpeech.Speaker == speaker {
					previouslyUnfinishedSpeech = PreviouslyUnfinishedSpeech{Present: false}
				}
				continue // Skip this speech and continue with others
			}

			logger.Info(fmt.Sprintf("Complete speech for Speaker %s (length=%d)", speaker, len(fullSpeech)))
			err = addTextToActivity(protocolId, speaker, fullSpeech, db, logger)
			if err != nil {
				return fmt.Errorf("failed to add completed speech to activity: %w", err)
			}
			updatedActivities++

			// If this completed a previously unfinished speech, clear it
			if previouslyUnfinishedSpeech.Present && previouslyUnfinishedSpeech.Speaker == speaker {
				previouslyUnfinishedSpeech = PreviouslyUnfinishedSpeech{Present: false}
			}
		}

		// Handle started_speech
		startedSpeechMap := resultMap["started_speech"].(map[string]any)
		startedSpeechPresent := startedSpeechMap["present"].(bool)

		if startedSpeechPresent {
			speaker := startedSpeechMap["speaker"].(string)
			beginOfSpeechStart := startedSpeechMap["begin_of_speech_start"].(string)
			beginningTooShort := startedSpeechMap["beginning_too_short"].(bool)

			previouslyUnfinishedSpeech = PreviouslyUnfinishedSpeech{
				Present:           true,
				Speaker:           speaker,
				SpeechStart:       beginOfSpeechStart,
				BeginningTooShort: beginningTooShort,
			}

			logger.Info(fmt.Sprintf("Started/continuing unfinished speech for Speaker %s", speaker))
		} else {
			previouslyUnfinishedSpeech = PreviouslyUnfinishedSpeech{Present: false}
		}

		logger.Info(fmt.Sprintf("Finished processing chunk from index %d to %d. Updated %d activities", i, end, updatedActivities))
		//if i >= 10_000 {
		//	break
		//}
	}

	logger.Info(fmt.Sprintf("Completed processing all chunks in %s", time.Since(startTime)))
	logger.Info(fmt.Sprintf("Total unmatched speeches (could not be matched at all): %d", unmatchedSpeechesCount))

	return nil
}

func assignSpeechesToActivitiesIterative() error {
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	consoleLogLevel := Debug
	databaseLogLevel := Debug
	logger := NewLogger(db, &consoleLogLevel, &databaseLogLevel)

	var protocols []Protocol
	// TODO only check for activities of the 'Rede' type
	//err = db.Select(&protocols, "SELECT * FROM protocols p WHERE EXISTS (SELECT 1 FROM activities a WHERE a.protocol_id = p.id AND a.text IS NULL OR a.text = '')")
	err = db.Select(&protocols, "SELECT * FROM protocols p WHERE p.ID = 5733 AND EXISTS (SELECT 1 FROM activities a WHERE a.protocol_id = p.id AND a.text IS NULL OR a.text = '')")
	if err != nil {
		logger.Error(fmt.Sprintf("failed to select protocols: %v", err))
		return fmt.Errorf("failed to select protocols: %w", err)
	}

	for _, protocol := range protocols {
		err = processSpeechesIterative(&protocol, db, logger)
		if err != nil {
			logger.Error(fmt.Sprintf("failed to process speeches for protocol %d: %v", protocol.ID, err))
			return fmt.Errorf("failed to process speeches for protocol %d: %w", protocol.ID, err)
		}
		logger.Info(fmt.Sprintf("Successfully processed speeches for protocol %d", protocol.ID))

		var count int
		err = db.Get(&count, "SELECT COUNT(*) FROM activities a WHERE a.protocol_id = $1 AND a.type = 'Rede' AND (a.text IS NULL OR a.text = '')", protocol.ID)
		if err != nil {
			logger.Error(fmt.Sprintf("failed to check remaining speeches for protocol %d: %v", protocol.ID, err))
			return fmt.Errorf("failed to check remaining speeches for protocol %d: %w", protocol.ID, err)
		}
		if count > 0 {
			logger.Warn(fmt.Sprintf("There are still %d speeches without text for protocol %d", count, protocol.ID))
		} else {
			logger.Info(fmt.Sprintf("All speeches have been assigned for protocol %d", protocol.ID))
		}
	}
	return nil
}
