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

const systemInstruction = `
You will receive a chunk of a German parliamentary protocol along with the end of the previous chunk. Your task is to extract speeches.
A speech is the full statement of one person, including any interruptions (e.g., questions or interjections by others), WHICH ALWAYS SHOULD BE INCLUDED AS PART OF THE SPEECH.

1. Identify speeches that start and end within this chunk (not the previous chunk). For each, return the speaker's full name, the first 15-30 words, and the last 15-30 words of the speech. If the beginning is very generic use closer to 30 words, else closer to 15.
2. If a speech starts in this chunk but does not end, return the speaker's full name and the first 15-30 words if possible.
3. If a speech started in a previous chunk and continues here, you already received the beginning of the speech. If the beginning is flagged as too short or generic, expand it with the beginning of this chunk until it is 15-30 words long in total. Make sure to not add any spaces or similar, the result should be verbatim from the original text.
Return the speech in the completed section if it is complete, or in the "started" section if it is still not complete.

"Speaker's full name" refers to the format "FirstName LastName" with no titles, honorifics, or party affiliations.
Speeches always begin with the speaker being named (e.g., "Dr. Angela Merkel (CDU/CSU):" or "Manuela Schwesig (Mecklenburg-Vorpommern):").
Note that questions or interjections also begin with a speaker name and should be included as part of the ongoing speech rather than separated.
DO NOT INCLUDE organizational remarks by the president or vice president of the parliament, they are not speeches.
'Erklärungen' also count as speeches if its done for or by a natural person. If the Erklärung is given for somebody else, affiliate it with that person. For example "Für Frau Ministerin Mona Neubaur gebe ich folgende
Erklärung zu Protokoll" would be a speech for Mona Neubaur. "Für die Länder Hamburg und Bremen gebe ich folgende Erklärung zu Protokoll" is not done for or by a natural person, and therefore must be ignored.

You must return the first 15-30 words and the last 15-30 words of each speech in the original formatting, so I can programatically reconstruct the full speech.
This uses less tokens than you returning the full speech. Therefore you must be CAREFUL TO MATCH EXACTLY THE ORIGINAL FORMATTING INCLUDING ANY ARTIFACTS LIKE NBSP AND ESPECIALLY NEWLINES.

CRITICAL RULES - EXACT SUBSTRING EXTRACTION:
- Copy text EXACTLY character by character from the original
- DO NOT use ".." or "..." (ellipses) to skip or summarize parts
- NEVER START OR END A TEXT WITH ".." or "..." (ellipses), except if this is actually in the original text
- DO NOT paraphrase, reformat, or reconstruct sentences
- DO NOT add or remove any characters, spaces, or newlines
- DO NOT CHANGE THE CASING OF ANY LETTER
- NEVER SKIP SPEECHES, WE NEED ALL OF THEM.
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
							Description: "The full name of the speaker that gave the speech. Do NOT INCLUDE titles like 'Dr.' or 'Prof.'",
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
						Description: "The full name of the speaker that gave the started speech. Do NOT INCLUDE titles like 'Dr.' or 'Prof.'",
					},
					"begin_of_speech_start": {
						Type:        "string",
						Description: "EXACT substring: The first 15-30 words of the speech copied character-by-character from the original text. NO ellipsis (..), NO paraphrasing, NO changes. Must include exact formatting with NBSP, newlines, and all artifacts. Also include interruptions by others like interjections or clapping if they are in the original text.",
					},
					"beginning_too_short": {
						Type:        "boolean",
						Description: "Indicates if the beginning of the speech is too short or generic because the chunk ended soon after the beginning. Only set to true if the beginning is very likely too generic to identify the text. ONLY do this if you are really sure that the beginning is too short, as this can lead to errors.",
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
				AND (a.type = 'Rede' OR a.type = 'Rede (zu Protokoll gegeben)')
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

	//TODO: Log in here, it seems some valid speeches are not correlated with the activity for an unknown reason without failing. Maybe we add an empty string? E.g. because escaping empties the speech?

	_, err = db.Exec("UPDATE activities SET text = $2 WHERE id = $1", activityId, strings.ToValidUTF8(speech, ""))
	if err != nil {
		return fmt.Errorf("failed to update activity %d with speech for speaker %s: %w", activityId, speaker, err)
	}
	return nil
}

func processSpeeches(protocol *Protocol, db DBInterface, logger *Logger) error {

	var unmatchedSpeechesCount int = 0
	protocolText := protocol.Text
	protocolId := protocol.ID
	client, err := getGeminiClient()

	if err != nil {
		return fmt.Errorf("failed to get Gemini client: %w", err)
	}

	systemInstruction := &genai.Content{
		Parts: []*genai.Part{
			{Text: systemInstruction},
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
				contextText += ". The previous chunk did not contain enough words of the new speech to provide an unambiguous start string. This chunk starts EXACTLY where the previous chunk ended. You must add a few words from the beginning of this chunk to speech_text_start until the total is 15-30 words. DO NOT ADD SPACES OR SIMILAR, APPEND IMMEDEATELY AT THE END OF speech_text_start."
			}
			contextText += "\n\n"
		} else {
			contextText = "The previous chunk did NOT contain an unfinished speech.\n\n"
		}

		if i > 0 {
			prevChunkStart := max(0, i-1000)
			endOfPreviousChunk := protocolText[prevChunkStart:i]
			contextText += "The end of the previous chunk is:\n" + endOfPreviousChunk + "\n\n"
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

		responseText := resp.Text()
		logger.Debug(fmt.Sprintf("Gemini model response (length=%d): %s", len(responseText), responseText))

		var resultMap map[string]any
		if err := json.Unmarshal([]byte(resp.Text()), &resultMap); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}

		logger.Debug(fmt.Sprintf("Parsed response: %+v", resultMap))

		completeSpeechesArray, ok := resultMap["complete_speeches"].([]any)
		if !ok {
			logger.Warn("complete_speeches is not an array")
		}
		for _, speechEntry := range completeSpeechesArray {
			speechMap := speechEntry.(map[string]any)
			speaker := speechMap["speaker"].(string)
			speechTextStart := speechMap["speech_text_start"].(string)
			speechTextEnd := speechMap["speech_text_end"].(string)

			// Reconstruct the full speech using start and end
			fullSpeech, err := getSpeechByStartAndEnd(speechTextStart, speechTextEnd, protocol, logger)
			if err != nil {
				logger.Warn(fmt.Sprintf("Skipping speech for speaker %s: %v", speaker, err))
				unmatchedSpeechesCount++
				if previouslyUnfinishedSpeech.Present && previouslyUnfinishedSpeech.Speaker == speaker {
					previouslyUnfinishedSpeech = PreviouslyUnfinishedSpeech{Present: false}
				}
				continue
			}

			logger.Info(fmt.Sprintf("Complete speech for Speaker %s (length=%d)", speaker, len(fullSpeech)))
			err = addTextToActivity(protocolId, speaker, fullSpeech, db, logger)
			if err != nil {
				return fmt.Errorf("failed to add completed speech to activity: %w", err)
			}
			updatedActivities++

			if previouslyUnfinishedSpeech.Present && previouslyUnfinishedSpeech.Speaker == speaker {
				previouslyUnfinishedSpeech = PreviouslyUnfinishedSpeech{Present: false}
			}
		}

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
	}
	logger.Info(fmt.Sprintf("Completed processing all chunks in %s", time.Since(startTime)))
	logger.Info(fmt.Sprintf("Total unmatched speeches (could not be matched at all): %d", unmatchedSpeechesCount))

	return nil
}

func processSingleProtocol(protocolId int) error {
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()
	loggerLevel := Debug
	logger := NewLogger(db, &loggerLevel, &loggerLevel)
	logger.AppendPrefix(fmt.Sprintf("protocol %d", protocolId))

	var protocol Protocol
	err = db.Get(&protocol, "SELECT * FROM protocols WHERE id = $1", protocolId)
	if err != nil {
		logger.Error(fmt.Sprintf("failed to get protocol: %v", err))
		return err
	}

	err = processSpeeches(&protocol, db, logger)
	if err != nil {
		logger.Error(fmt.Sprintf("failed to process speeches: %v", err))
		return fmt.Errorf("failed to process speeches: %w", err)
	}
	logger.Info(fmt.Sprintf("Successfully processed speeches for protocol %d", protocolId))
	return nil
}

func processNextProtocol(logger *Logger) (bool, error) {
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		return true, fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	if logger == nil {
		logger = NewLogger(db, nil, nil)
	}

	var protocols []Protocol
	query := `
		WITH to_update AS (
			SELECT p.id
			FROM protocols p
			WHERE
				(
					EXISTS (
						SELECT 1
						FROM activities a
						WHERE a.protocol_id = p.id AND (a.text IS NULL OR a.text = '') AND (a.type = 'Rede' OR a.type = 'Rede (zu Protokoll gegeben)')
					)
						AND p.processing_status = 'not_started'
					)
			OR (
				p.processing_status = 'failed'
					AND (
					p.failed_count = 1
						OR (
						p.failed_count = 2
							AND (now() - p.processing_timestamp > interval '1 hour')
						)
					)
				)
			OR (
				p.processing_status = 'in_progress'
					AND (now() - p.processing_timestamp > interval '1 hour')
				)
			AND p.text IS NOT NULL AND p.text != '' AND p.text != '[NoTextAvailable]' AND len(p.text) > 1000
			ORDER BY p.date DESC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE protocols p
		SET processing_status = 'in_progress', 
			processing_timestamp = now()
		FROM to_update t
		WHERE p.id = t.id
		RETURNING p.*;
		`
	handleError := func(protocolID int, err error, logger *Logger) {
		logger.Error(fmt.Sprintf("failed to process speeches for protocol %d: %v", protocolID, err))
		_, err = db.Exec("UPDATE protocols SET processing_status = 'failed', failed_count = failed_count + 1 WHERE id = $1", protocolID)
		if err != nil {
			logger.Error(fmt.Sprintf("failed to update protocol %d: %v", protocolID, err))
		}
	}

	err = db.Select(&protocols, query)
	if err != nil {
		logger.Error(fmt.Sprintf("failed to select protocols: %v", err))
		return true, fmt.Errorf("failed to select protocols: %w", err)
	}

	//Len at most 1
	for _, protocol := range protocols {
		logger.AppendPrefix(fmt.Sprintf("protocol %d", protocol.ID))

		logger.Info(fmt.Sprintf("Processing speeches for protocol %d", protocol.ID))
		db.Exec("UPDATE protocols SET processing_status = 'in_progress', processing_timestamp = now() WHERE id = $1", protocol.ID)
		tx, err := db.Beginx()
		if err != nil {
			err = fmt.Errorf("failed to begin transaction: %w", err)
			handleError(protocol.ID, err, logger)
			continue
		}
		defer tx.Rollback()

		err = processSpeeches(&protocol, tx, logger)
		if err != nil {
			err = fmt.Errorf("failed to process speeches: %w", err)
			handleError(protocol.ID, err, logger)
			continue
		}

		var count int
		err = db.Get(&count, "SELECT COUNT(*) FROM activities a WHERE a.protocol_id = $1 AND (a.type = 'Rede' OR a.type = 'Rede (zu Protokoll gegeben)') AND (a.text IS NULL OR a.text = '')", protocol.ID)
		if err != nil {
			err = fmt.Errorf("failed to check remaining speeches: %w", err)
			handleError(protocol.ID, err, logger)
			continue
		}
		if count > 0 {
			logger.Warn(fmt.Sprintf("There are still %d speeches without text for protocol %d", count, protocol.ID))
		} else {
			logger.Info(fmt.Sprintf("All speeches have been assigned for protocol %d", protocol.ID))
		}

		err = tx.Commit() //TODO: Commit only if missing activity-rate is at most 20 or 25%, else fail
		if err != nil {
			err = fmt.Errorf("failed to commit transaction: %w", err)
			handleError(protocol.ID, err, logger)
			continue
		}
		logger.Info(fmt.Sprintf("Successfully processed speeches for protocol %d", protocol.ID))
		db.Exec("UPDATE protocols SET processing_status = 'completed' WHERE id = $1", protocol.ID)
		if err != nil {
			logger.Error(fmt.Sprintf("failed to set processed protocol %d to completed: %v", protocol.ID, err))
			continue
		}
	}
	return len(protocols) == 0, nil
}
