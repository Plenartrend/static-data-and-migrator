package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jmoiron/sqlx"
	"google.golang.org/genai"
)

type PreviouslyUnfinishedSpeech struct {
	Present bool
	Speaker string
	Speech  string
}

const iterativeInstructionText = `
You will receive a chunk of a German parliamentary protocol. Your task is to extract speeches.
A speech is the full statement of one person, including any interruptions (e.g., questions or interjections by others), which should be included as part of the speech.

1. Identify speeches that start and end within this chunk. For each, return the speaker's full name and the complete speech text.
2. If a speech starts in this chunk but does not end, return the speaker's full name and the partial speech.
3. If a speech started in a previous chunk and continues here, return the continuation and indicate whether the speech is now complete or still ongoing.

"Speaker's full name" refers to the format "FirstName LastName" with no titles, honorifics, or party affiliations.
Speeches always begin with the speaker being named (e.g., "Dr. Angela Merkel (CDU/CSU):").
Note that questions or interjections also begin with a speaker name and should be included as part of the ongoing speech rather than separated.
Do not include "speeches" by the president or vice president of the parliament.
Maintain the original language and formatting of the speech text exactly as in the protocol.
`

const chunkSize = 25_000

func getResponseSchema() *genai.Schema {
	return &genai.Schema{
		Type:        "object",
		Description: "Container for iterative speech extraction and assignment to activities.",
		Properties: map[string]*genai.Schema{
			"continued_speech": {
				Type:        "object",
				Description: "Contains the continuation part of a potential incomplete speech started in the previous chunk that is continued in this chunk.",
				Properties: map[string]*genai.Schema{
					"present": {
						Type:        "boolean",
						Description: "Indicates if a previously incompleted speech is present in this chunk.",
					},
					"completed": {
						Type:        "boolean",
						Description: "Indicates if the previously incompleted speech is now completed in this chunk.",
					},
					"rest_of_speech": {
						Type:        "string",
						Description: "The remaining part of a previously started but not yet completed speech in original language and formatting.",
					},
				},
			},
			"complete_speeches": {
				Type:        "array",
				Description: "List of speeches that both start and end within this chunk.",
				Items: &genai.Schema{
					Type: "object",
					Properties: map[string]*genai.Schema{
						"speaker": {
							Type:        "string",
							Description: "The full name of the speaker that gave the speech.",
						},
						"speech_text": {
							Type:        "string",
							Description: "Complete speech text in original language and formatting.",
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
						Description: "The full name of the speaker that gave the started speech.",
					},
					"begin_of_speech": {
						Type:        "string",
						Description: "The beginning part of the speech that has started, in original language and formatting.",
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

	_, err = db.Exec("UPDATE activities SET text = $2 WHERE id = $1", activityId, speech)
	if err != nil {
		return fmt.Errorf("failed to update activity %d with speech for speaker %s: %w", activityId, speaker, err)
	}
	return nil
}

// TODO use db transaction
func processSpeechesIterative(protocolId int, protocolText string, db DBInterface, logger *Logger) error {
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
		Present: false,
		Speaker: "",
		Speech:  "",
	}

	startTime := time.Now()

	for i := 0; i < len(protocolText); i += chunkSize {
		end := min(i+chunkSize, len(protocolText))
		chunk := protocolText[i:end]

		logger.Debug(fmt.Sprintf("Processing chunk from index %d to %d", i, end))

		var unfinishedSpeechText string
		if previouslyUnfinishedSpeech.Present {
			unfinishedSpeechText = "The previous chunk contained an unfinished speech by " + previouslyUnfinishedSpeech.Speaker + "\n"
		} else {
			unfinishedSpeechText = "The previous chunk did NOT contain an unfinished speech.\n"
		}

		content := []*genai.Content{
			{
				Parts: []*genai.Part{
					{
						Text: unfinishedSpeechText,
					},
					{
						Text: "The next protocol chunk is:\n",
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

		// Handle continued_speech
		continuedSpeechMap := resultMap["continued_speech"].(map[string]any)
		continuedSpeechPresent := continuedSpeechMap["present"].(bool)

		if continuedSpeechPresent {
			completed := continuedSpeechMap["completed"].(bool)
			restOfSpeech := continuedSpeechMap["rest_of_speech"].(string)

			if completed {
				// Append to previous speech and mark as complete
				previouslyUnfinishedSpeech.Speech += restOfSpeech
				logger.Info(fmt.Sprintf("Completed speech: %s", previouslyUnfinishedSpeech.Speech))
				err := addTextToActivity(protocolId, previouslyUnfinishedSpeech.Speaker, previouslyUnfinishedSpeech.Speech, db, logger)
				if err != nil {
					return fmt.Errorf("failed to add completed speech to activity: %w", err)
				}

				previouslyUnfinishedSpeech = PreviouslyUnfinishedSpeech{Present: false}
			} else {
				// Append to previous speech and continue
				previouslyUnfinishedSpeech.Speech += restOfSpeech
				logger.Info(fmt.Sprintf("Continuing speech by Speaker %s: %s", previouslyUnfinishedSpeech.Speaker, previouslyUnfinishedSpeech.Speech))
			}
		}

		// Handle complete_speeches
		completeSpeechesArray := resultMap["complete_speeches"].([]any)
		for _, speechEntry := range completeSpeechesArray {
			speechMap := speechEntry.(map[string]any)
			speaker := speechMap["speaker"].(string)
			speechText := speechMap["speech_text"].(string)

			// Here you would save the complete speech to the database or desired storage
			logger.Info(fmt.Sprintf("Complete speech for Speaker %s: %s", speaker, speechText))
			err := addTextToActivity(protocolId, speaker, speechText, db, logger)
			if err != nil {
				return fmt.Errorf("failed to add completed speech to activity: %w", err)
			}
		}

		// Handle started_speech
		startedSpeechMap := resultMap["started_speech"].(map[string]any)
		startedSpeechPresent := startedSpeechMap["present"].(bool)

		if startedSpeechPresent {
			speaker := startedSpeechMap["speaker"].(string)
			beginOfSpeech := startedSpeechMap["begin_of_speech"].(string)

			previouslyUnfinishedSpeech = PreviouslyUnfinishedSpeech{
				Present: true,
				Speaker: speaker,
				Speech:  beginOfSpeech,
			}

			logger.Info(fmt.Sprintf("Started new unfinished speech for Speaker %s: %s", speaker, beginOfSpeech))
		} else {
			previouslyUnfinishedSpeech = PreviouslyUnfinishedSpeech{Present: false}
		}

		logger.Info(fmt.Sprintf("Finished processing chunk from index %d to %d", i, end))
		if (i / chunkSize) > 2 {
			break
		}
	}

	logger.Info(fmt.Sprintf("Completed processing all chunks in %s", time.Since(startTime)))

	return nil
}

func assignSpeechesToActivitiesIterative() error {
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	consoleLogLevel := Debug
	logger := NewLogger(db, &consoleLogLevel, nil)

	var protocols []Protocol
	// TODO only check for activities of the 'Rede' type
	//err = db.Select(&protocols, "SELECT * FROM protocols p WHERE EXISTS (SELECT 1 FROM activities a WHERE a.protocol_id = p.id AND a.text IS NULL OR a.text = '')")
	err = db.Select(&protocols, "SELECT * FROM protocols p WHERE p.ID = 5626 AND EXISTS (SELECT 1 FROM activities a WHERE a.protocol_id = p.id AND a.text IS NULL OR a.text = '')")
	if err != nil {
		logger.Error(fmt.Sprintf("failed to select protocols: %v", err))
		return fmt.Errorf("failed to select protocols: %w", err)
	}

	for _, protocol := range protocols {
		err = processSpeechesIterative(protocol.ID, protocol.Text, db, logger)
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
