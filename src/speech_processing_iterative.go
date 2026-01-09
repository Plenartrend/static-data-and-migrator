package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jmoiron/sqlx"
	"google.golang.org/genai"
)

type SpeechMetaData struct {
	ActivityID  int
	SpeakerName string
}

type PreviouslyUnfinishedSpeech struct {
	Present    bool
	Speaker    string
	ActivityID int
	Speech     string
}

const iterativeInstructionText = `You are an expert at extracting german speeches from german parliamentary protocols and assigning them to the correct activities based on speaker information.
An activity is like a container for a speech and has an ID and a speaker.
You will receive a chunk of a parliamentary protocol along with a list of activities and their associated speakers. The speeches are in the same order as the IDs of the activities.
One speech is defined as a full speech by one person, including all questions and answers.
Your task is to:
1. Identify speeches that start and end within the given chunk. For each complete speech, return the activity ID it belongs to and the full speech text.
2. If there is a speech that starts in this chunk but does not end (i.e., it is incomplete), return the activity ID and the beginning part of the speech.
3. If there was a speech that started in a previous chunk and is continued in this chunk, append the continuation to that speech. Indicate whether this previously unfinished speech is now completed or still ongoing.
Note that you must extract the speeches from start to finish, truncating anything like parts from other speeches at the beginning and end.
Ensure that all returned speech texts maintain the original language and formatting as found in the protocol.`

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
						"activity_id": {
							Type:        "integer",
							Description: "The ID of the activity the speech belongs to.",
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
					"activity_id": {
						Type:        "integer",
						Description: "The ID of the activity for the started speech.",
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

func addTextToActivity(db DBInterface, id int, speech string) error {
	_, err := db.Exec("UPDATE activities SET text = $2 WHERE id = $1", id, speech)
	if err != nil {
		return fmt.Errorf("failed to update activity with completed speech: %w", err)
	}
	return nil
}

func removeById(speeches []SpeechMetaData, id int) []SpeechMetaData {
	for i, speech := range speeches {
		if speech.ActivityID == id {
			return append(speeches[:i], speeches[i+1:]...)
		}
	}
	return speeches
}

// TODO use db transaction
func processSpeechesIterative(protocolText string, speechMetaData []SpeechMetaData, db DBInterface, logger *Logger) error {
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
		Present:    false,
		Speaker:    "",
		ActivityID: 0,
		Speech:     "",
	}

	startTime := time.Now()

	for i := 0; i < len(protocolText); i += chunkSize {
		end := min(i+chunkSize, len(protocolText))
		chunk := protocolText[i:end]

		logger.Debug(fmt.Sprintf("Processing chunk from index %d to %d", i, end))

		var parts []*genai.Part

		var activitiesPrompt genai.Part = genai.Part{
			Text: "--------These are the Activities: --------\n",
		}
		parts = append(parts, &activitiesPrompt)

		for _, activity := range speechMetaData {
			parts = append(parts, &genai.Part{
				Text: fmt.Sprintf("Activity ID: %d, Speaker: %s\n", activity.ActivityID, activity.SpeakerName),
			})
		}

		parts = append(parts, &genai.Part{
			Text: "--------This is the information regarding a potential previously unfinished speech: --------\n",
		})

		var text string
		if previouslyUnfinishedSpeech.Present {
			text = "The previous chunk contained an unfinished speech by " + previouslyUnfinishedSpeech.Speaker + " with activity ID " + fmt.Sprintf("%d", previouslyUnfinishedSpeech.ActivityID) + ".\n"
		} else {
			text = "The previous chunk did NOT contain an unfinished speech.\n"
		}

		parts = append(parts, &genai.Part{
			Text: text,
		})

		parts = append(parts, &genai.Part{
			Text: "--------This is the next protocol chunk: --------\n",
		})

		parts = append(parts, &genai.Part{
			Text: chunk,
		})

		resp, err := client.Models.GenerateContent(context.Background(), geminiModel, []*genai.Content{
			{
				Parts: parts,
			},
		}, &genai.GenerateContentConfig{
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
				// Here you would save the completed speech to the database or desired storage
				logger.Info(fmt.Sprintf("Completed speech for Activity ID %d: %s", previouslyUnfinishedSpeech.ActivityID, previouslyUnfinishedSpeech.Speech))
				err := addTextToActivity(db, previouslyUnfinishedSpeech.ActivityID, previouslyUnfinishedSpeech.Speech)
				if err != nil {
					return fmt.Errorf("failed to add completed speech to activity: %w", err)
				}
				speechMetaData = removeById(speechMetaData, previouslyUnfinishedSpeech.ActivityID)

				previouslyUnfinishedSpeech = PreviouslyUnfinishedSpeech{Present: false}
			} else {
				// Append to previous speech and continue
				previouslyUnfinishedSpeech.Speech += restOfSpeech
				logger.Info(fmt.Sprintf("Continuing speech for Activity ID %d: %s", previouslyUnfinishedSpeech.ActivityID, previouslyUnfinishedSpeech.Speech))
			}
		}

		// Handle complete_speeches
		completeSpeechesArray := resultMap["complete_speeches"].([]any)
		for _, speechEntry := range completeSpeechesArray {
			speechMap := speechEntry.(map[string]any)
			activityID := int(speechMap["activity_id"].(float64))
			speechText := speechMap["speech_text"].(string)

			// Here you would save the complete speech to the database or desired storage
			logger.Info(fmt.Sprintf("Complete speech for Activity ID %d: %s", activityID, speechText))
			err := addTextToActivity(db, activityID, speechText)
			if err != nil {
				return fmt.Errorf("failed to add completed speech to activity: %w", err)
			}
			speechMetaData = removeById(speechMetaData, activityID)
		}

		// Handle started_speech
		startedSpeechMap := resultMap["started_speech"].(map[string]any)
		startedSpeechPresent := startedSpeechMap["present"].(bool)

		if startedSpeechPresent {
			activityID := int(startedSpeechMap["activity_id"].(float64))
			beginOfSpeech := startedSpeechMap["begin_of_speech"].(string)

			previouslyUnfinishedSpeech = PreviouslyUnfinishedSpeech{
				Present:    true,
				ActivityID: activityID,
				Speech:     beginOfSpeech,
			}

			logger.Info(fmt.Sprintf("Started new unfinished speech for Activity ID %d: %s", activityID, beginOfSpeech))
		} else {
			previouslyUnfinishedSpeech = PreviouslyUnfinishedSpeech{Present: false}
		}

		logger.Info(fmt.Sprintf("Finished processing chunk from index %d to %d", i, end))
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
	//err = db.Select(&protocols, "SELECT * FROM protocols p WHERE EXISTS (SELECT 1 FROM activities a WHERE a.protocol_id = p.id AND a.text IS NULL OR a.text = '')")
	err = db.Select(&protocols, "SELECT * FROM protocols p WHERE p.ID = 5626 AND EXISTS (SELECT 1 FROM activities a WHERE a.protocol_id = p.id AND a.text IS NULL OR a.text = '')")
	if err != nil {
		logger.Error(fmt.Sprintf("failed to select protocols: %v", err))
		return fmt.Errorf("failed to select protocols: %w", err)
	}

	for _, protocol := range protocols {
		var activities []Activity
		err = db.Select(&activities, "SELECT * FROM activities a WHERE protocol_id = $1 AND a.type = 'Rede' AND (text IS NULL OR text = '')", protocol.ID)
		logger.Debug(fmt.Sprintf("Found %d activities for protocol %d", len(activities), protocol.ID))
		if err != nil {
			logger.Error(fmt.Sprintf("failed to select activities: %v", err))
			return fmt.Errorf("failed to select activities: %w", err)
		}
		var activitiesMetaData []SpeechMetaData

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
			activitiesMetaData = append(activitiesMetaData, SpeechMetaData{
				ActivityID:  activity.ID,
				SpeakerName: speakerName,
			})
		}

		err = processSpeechesIterative(protocol.Text, activitiesMetaData, db, logger)
		if err != nil {
			logger.Error(fmt.Sprintf("failed to process speeches for protocol %d: %v", protocol.ID, err))
			return fmt.Errorf("failed to process speeches for protocol %d: %w", protocol.ID, err)
		}
		logger.Info(fmt.Sprintf("Successfully processed speeches for protocol %d", protocol.ID))
	}
	return nil
}
