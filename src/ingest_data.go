package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	dip "plenartrend/static-data-and-migrator/src/openAPI"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

const requestTimeout = 100 * time.Millisecond
const ingestionSleepTime = 1 * time.Second

// DBInterface allows using either *sqlx.DB or *sqlx.Tx
type DBInterface interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Get(dest interface{}, query string, args ...interface{}) error
	Select(dest interface{}, query string, args ...interface{}) error
	QueryRow(query string, args ...interface{}) *sql.Row
}

var ingestionWG sync.WaitGroup
var ingestionsTasks = make(chan func())
var workerOnce sync.Once

func initIngestionWorker() {
	workerOnce.Do(func() {
		go func() {
			for task := range ingestionsTasks {
				task()
				ingestionWG.Done()
			}
		}()
	})
}

func ingestPersons(client *dip.ClientWithResponses, lastSuccessTimestamp time.Time, currentTimestamp time.Time, db DBInterface, logger *Logger) error {
	logger.Debug("Ingesting persons")
	var count = 0
	var cursor *string

	for {
		resp, err := client.GetPersonListWithResponse(context.Background(), &dip.GetPersonListParams{
			FAktualisiertStart: &lastSuccessTimestamp,
			FAktualisiertEnd:   &currentTimestamp,
			Cursor:             cursor,
		})
		if err != nil {
			return fmt.Errorf("failed to ingest persons: %w", err)
		}
		if resp.JSON200 == nil {
			return fmt.Errorf("unexpected response status: %d", resp.StatusCode())
		}

		// Capture documents in closure for parallel ingestion
		documents := resp.JSON200.Documents

		if cursor != nil && *cursor == resp.JSON200.Cursor {
			break
		}

		ingestionWG.Add(1)
		ingestionsTasks <- func() {
			err := processPersons(db, documents, logger)
			if err != nil {
				logger.Error(fmt.Sprintf("failed to process persons: %v", err))
			}
		}

		count += len(documents)
		cursor = &resp.JSON200.Cursor
		logger.Debug(fmt.Sprintf("Got %d persons with cursor %s from total %d", count, *cursor, resp.JSON200.NumFound))
		time.Sleep(requestTimeout)
	}
	return nil
}

func processPersons(db DBInterface, persons []dip.Person, logger *Logger) error {
	for _, p := range persons {
		_, err := db.Exec("INSERT INTO persons (id, api_updated) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET api_updated = $2", p.Id, p.Aktualisiert)
		if err != nil {
			return fmt.Errorf("insert person %s: %w", p.Id, err)
		}

		// ----Validate required fields----
		if len(p.Funktion) != 1 {
			return fmt.Errorf("person %s: expected 1 Funktion, got %d", p.Id, len(p.Funktion))
		}
		if p.Fraktion != nil && len(*p.Fraktion) > 1 {
			return fmt.Errorf("person %s: expected at most 1 Fraktion, got %d", p.Id, len(*p.Fraktion))
		}
		// ---End of validate required fields----

		var groupId *int
		if p.Fraktion != nil && len(*p.Fraktion) > 0 {
			fraktion := (*p.Fraktion)[0]
			id, err := getOrSetGroupByName(db, nil, &fraktion, logger)
			if err != nil {
				return fmt.Errorf("get group for person %s: %w", p.Id, err)
			}
			groupId = &id
		}

		// Create a role for each Wahlperiode (or -1 if missing)
		wahlperioden := []int32{-1}
		if p.Wahlperiode != nil && len(*p.Wahlperiode) > 0 {
			wahlperioden = *p.Wahlperiode
		} else {
			logger.Warn(fmt.Sprintf("person %s has no Wahlperiode, using -1", p.Id))
		}

		for _, wp := range wahlperioden {
			electionPeriod, err := getOrSetElectionPeriod(db, int(wp), logger)
			if err != nil {
				return fmt.Errorf("get election period for person %s: %w", p.Id, err)
			}

			if err := setRole(db, p.Id, p.Funktion[0], p.Nachname, p.Vorname, electionPeriod, groupId, logger); err != nil {
				return fmt.Errorf("insert role for person %s: %w", p.Id, err)
			}
		}

		if p.PersonRoles != nil {
			for _, r := range *p.PersonRoles {
				var groupId *int
				if r.Fraktion != nil {
					id, err := getOrSetGroupByName(db, nil, r.Fraktion, logger)
					if err != nil {
						return fmt.Errorf("get group for role: %w", err)
					}
					groupId = &id
				}

				// Use -1 if no WahlperiodeNummer
				roleWahlperioden := []int32{-1}
				if r.WahlperiodeNummer != nil && len(*r.WahlperiodeNummer) > 0 {
					roleWahlperioden = *r.WahlperiodeNummer
				} else {
					logger.Warn(fmt.Sprintf("role for person %s has no WahlperiodeNummer, using -1", p.Id))
				}

				for _, wp := range roleWahlperioden {
					electionPeriod, err := getOrSetElectionPeriod(db, int(wp), logger)
					if err != nil {
						return fmt.Errorf("get election period for role: %w", err)
					}
					if err := setRole(db, p.Id, r.Funktion, r.Nachname, r.Vorname, electionPeriod, groupId, logger); err != nil {
						return fmt.Errorf("insert role: %w", err)
					}
				}
			}
		}
	}
	return nil
}

func ingestProtocols(client *dip.ClientWithResponses, lastSuccessTimestamp time.Time, currentTimestamp time.Time, db DBInterface, logger *Logger) error {
	logger.Debug("Ingesting protocols")
	var count = 0
	var cursor *string

	for {
		resp, err := client.GetPlenarprotokollTextListWithResponse(context.Background(), &dip.GetPlenarprotokollTextListParams{
			FAktualisiertStart: &lastSuccessTimestamp,
			FAktualisiertEnd:   &currentTimestamp,
			Cursor:             cursor,
		})
		if err != nil {
			return fmt.Errorf("failed to ingest protocols: %w", err)
		}
		if resp.JSON200 == nil {
			return fmt.Errorf("unexpected response status: %d", resp.StatusCode())
		}

		// Capture documents in closure for parallel ingestion
		documents := resp.JSON200.Documents

		if cursor != nil && *cursor == resp.JSON200.Cursor {
			break
		}

		ingestionWG.Add(1)
		ingestionsTasks <- func() {
			err := processProtocols(documents, db, logger)
			if err != nil {
				logger.Error(fmt.Sprintf("failed to process protocols: %v", err))
			}
		}

		count += len(documents)
		cursor = &resp.JSON200.Cursor
		logger.Debug(fmt.Sprintf("Got %d protocols with cursor %s from total %d", count, *cursor, resp.JSON200.NumFound))
		time.Sleep(requestTimeout)
	}
	return nil
}

func processProtocols(protocols []dip.PlenarprotokollText, db DBInterface, logger *Logger) error {
	for _, p := range protocols {
		var publisher Body
		if IsValidBody(string(p.Herausgeber)) {
			publisher = Body(p.Herausgeber)
		} else {
			return fmt.Errorf("invalid body value: %s", p.Herausgeber)
		}

		electionPeriod := sql.NullInt32{Valid: false}
		if p.Wahlperiode != nil {
			ep, err := getOrSetElectionPeriod(db, int(*p.Wahlperiode), logger)
			if err != nil {
				return fmt.Errorf("error getting/setting election period for protocol %s: %w", p.Id, err)
			}
			electionPeriod = sql.NullInt32{Int32: int32(ep), Valid: true}
		}

		_, err := db.Exec(`
			INSERT INTO protocols
			(id, title, document_number, publisher, session_note, url, text, election_period, date, api_updated)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (id) DO UPDATE SET
				title = $2, document_number = $3, publisher = $4, session_note = $5, url = $6, text = $7, election_period = $8, date = $9,
				api_updated = $10
		`, p.Id, p.Titel, p.Dokumentnummer, publisher, p.Sitzungsbemerkung, p.Fundstelle.PdfUrl, sanitizeStringPtr(p.Text), electionPeriod, p.Datum.Time,
			p.Aktualisiert)

		if err != nil {
			return fmt.Errorf("failed to insert protocol %s: %w", p.Id, err)
		}
	}

	return nil
}

func ingestPrintedPapers(client *dip.ClientWithResponses, lastSuccessTimestamp time.Time, currentTimestamp time.Time, db DBInterface, logger *Logger) error {
	logger.Debug("Ingesting printed papers")
	var count = 0
	var cursor *string

	for {
		resp, err := client.GetDrucksacheTextListWithResponse(context.Background(), &dip.GetDrucksacheTextListParams{
			FAktualisiertStart: &lastSuccessTimestamp,
			FAktualisiertEnd:   &currentTimestamp,
			Cursor:             cursor,
		})
		if err != nil {
			return fmt.Errorf("failed to ingest printed papers: %w", err)
		}
		if resp.JSON200 == nil {
			return fmt.Errorf("unexpected response status: %d", resp.StatusCode())
		}

		// Capture documents in closure for parallel ingestion
		documents := resp.JSON200.Documents

		if cursor != nil && *cursor == resp.JSON200.Cursor {
			break
		}

		ingestionWG.Add(1)
		ingestionsTasks <- func() {
			err := processPrintedPapers(documents, db, logger)
			if err != nil {
				logger.Error(fmt.Sprintf("failed to process printed papers: %v", err))
			}
		}

		count += len(documents)
		cursor = &resp.JSON200.Cursor
		logger.Debug(fmt.Sprintf("Got %d printed papers with cursor %s from total %d", count, *cursor, resp.JSON200.NumFound))
		time.Sleep(requestTimeout)
	}

	return nil
}

func processPrintedPapers(printedPapers []dip.DrucksacheText, db DBInterface, logger *Logger) error {
	for _, p := range printedPapers {
		electionPeriod, err := getOrSetElectionPeriod(db, int(*p.Wahlperiode), logger)
		if err != nil {
			return fmt.Errorf("error getting/setting election period for protocol %s: %w", p.Id, err)
		}

		var groupId *int
		if len(p.Fundstelle.Urheber) == 0 {
			logger.Info(fmt.Sprintf("printed paper %s has no Urheber", p.Id))
		} else {
			if len(p.Fundstelle.Urheber) != 1 {
				logger.Warn(fmt.Sprintf("printed paper %s has %d Urheber, expected 1, using first", p.Id, len(p.Fundstelle.Urheber)))
			}
			gid, err := getOrSetGroupByName(db, nil, &p.Fundstelle.Urheber[0], logger)
			if err != nil {
				return fmt.Errorf("error getting/setting group for printed paper %s: %w", p.Id, err)
			}
			groupId = &gid
		}

		_, err = db.Exec(`
			INSERT INTO printed_papers
			(id, type, title, document_number, publisher, group_id, url, text, election_period, date, api_updated)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (id) DO UPDATE SET
				type = $2, title = $3, document_number = $4, publisher = $5, group_id = $6, url = $7, text = $8, election_period = $9, date = $10,
				api_updated = $11
		`, p.Id, p.Drucksachetyp, p.Titel, p.Dokumentnummer, p.Herausgeber, groupId, p.Fundstelle.PdfUrl, sanitizeStringPtr(p.Text), electionPeriod, p.Datum.Time,
			p.Aktualisiert)

		if err != nil {
			return fmt.Errorf("failed to insert printed paper %s: %w", p.Id, err)
		}
	}

	return nil
}

func ingestActivities(client *dip.ClientWithResponses, lastSuccessTimestamp time.Time, currentTimestamp time.Time, db DBInterface, logger *Logger) error {
	logger.Debug("Ingesting activities")
	var count = 0
	var cursor *string

	for {
		resp, err := client.GetAktivitaetListWithResponse(context.Background(), &dip.GetAktivitaetListParams{
			FAktualisiertStart: &lastSuccessTimestamp,
			FAktualisiertEnd:   &currentTimestamp,
			Cursor:             cursor,
		})
		if err != nil {
			return fmt.Errorf("failed to ingest activities: %w", err)
		}
		if resp.JSON200 == nil {
			return fmt.Errorf("unexpected response status: %d", resp.StatusCode())
		}

		// Capture documents in closure for parallel ingestion
		documents := resp.JSON200.Documents

		if cursor != nil && *cursor == resp.JSON200.Cursor {
			break
		}

		ingestionWG.Add(1)
		ingestionsTasks <- func() {
			err := processActivities(documents, db, logger)
			if err != nil {
				logger.Error(fmt.Sprintf("failed to process activities: %v", err))
			}
		}

		count += len(documents)
		cursor = &resp.JSON200.Cursor
		logger.Debug(fmt.Sprintf("Got %d activities with cursor %s from total %d", count, *cursor, resp.JSON200.NumFound))
		time.Sleep(requestTimeout)
	}
	return nil
}

func processActivities(activities []dip.Aktivitaet, db DBInterface, logger *Logger) error {
	logger.Debug(fmt.Sprintf("processing %d activities", len(activities)))
	for _, a := range activities {
		electionPeriod, err := getOrSetElectionPeriod(db, int(a.Wahlperiode), logger)
		if err != nil {
			return fmt.Errorf("error getting/setting election period for activity %s: %w", a.Id, err)
		}

		var roleId int
		err = db.Get(&roleId, `SELECT id from roles where person_id = $1 and election_period = $2`, a.PersonId, electionPeriod)
		if err != nil {
			logger.Warn(fmt.Sprintf("skipping activity %s: role not found for person %s in election period %d", a.Id, a.PersonId, electionPeriod))
			continue
		}

		documentTypeMap := map[dip.AktivitaetDokumentart]DocumentType{
			"Plenarprotokoll": DocumentProtocol,
			"Drucksache":      DocumentPrintedPaper,
		}
		documentType, ok := documentTypeMap[a.Dokumentart]
		if !ok {
			logger.Warn(fmt.Sprintf("skipping activity %s: invalid document type: %s", a.Id, a.Dokumentart))
			continue
		}

		printedPaperId := sql.NullInt32{Valid: false}
		protocolId := sql.NullInt32{Valid: false}

		if documentType == DocumentPrintedPaper {
			var ppId int32
			err = db.Get(&ppId, "SELECT id FROM printed_papers WHERE id=$1", a.Fundstelle.Id)
			if err != nil {
				logger.Warn(fmt.Sprintf("skipping activity %s: printed paper %s not found", a.Id, a.Fundstelle.Id))
				continue
			}
			printedPaperId = sql.NullInt32{Int32: ppId, Valid: true}
		} else {
			var pId int32
			err = db.Get(&pId, "SELECT id FROM protocols WHERE id=$1", a.Fundstelle.Id)
			if err != nil {
				logger.Warn(fmt.Sprintf("skipping activity %s: protocol %s not found", a.Id, a.Fundstelle.Id))
				continue
			}
			protocolId = sql.NullInt32{Int32: pId, Valid: true}
		}

		var text string = "" // TODO what is the text of the activity?

		_, err = db.Exec(`
			INSERT INTO activities
			(id, type, role_id, document_type, printed_paper_id, protocol_id, text, api_updated)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (id) DO UPDATE SET
				type = $2, role_id = $3, document_type = $4, printed_paper_id = $5, protocol_id = $6, text = $7, api_updated = $8
		`, a.Id, a.Aktivitaetsart, roleId, documentType, printedPaperId, protocolId, text, a.Aktualisiert)

		if err != nil {
			return fmt.Errorf("failed to insert activity %s: %w", a.Id, err)
		}
	}

	return nil
}

func ingestProcesses(client *dip.ClientWithResponses, lastSuccessTimestamp time.Time, currentTimestamp time.Time, db DBInterface, logger *Logger) error {
	logger.Debug("Ingesting processes")
	var count = 0
	var cursor *string

	for {
		resp, err := client.GetVorgangListWithResponse(context.Background(), &dip.GetVorgangListParams{
			FAktualisiertStart: &lastSuccessTimestamp,
			FAktualisiertEnd:   &currentTimestamp,
			Cursor:             cursor,
		})
		if err != nil {
			return fmt.Errorf("failed to ingest processes: %w", err)
		}
		if resp.JSON200 == nil {
			return fmt.Errorf("unexpected response status: %d", resp.StatusCode())
		}

		// Capture documents in closure for parallel ingestion
		documents := resp.JSON200.Documents

		if cursor != nil && *cursor == resp.JSON200.Cursor {
			break
		}

		ingestionWG.Add(1)
		ingestionsTasks <- func() {
			err := processProcesses(documents, db, logger)
			if err != nil {
				logger.Fatal(fmt.Sprintf("failed to process processes: %v", err))
			}
		}
		count += len(documents)
		cursor = &resp.JSON200.Cursor
		logger.Debug(fmt.Sprintf("Got %d processes with cursor %s from total %d", count, *cursor, resp.JSON200.NumFound))
		time.Sleep(requestTimeout)
	}
	return nil
}

func processProcesses(processes []dip.Vorgang, db DBInterface, logger *Logger) error {
	for _, p := range processes {
		electionPeriod, err := getOrSetElectionPeriod(db, int(p.Wahlperiode), logger)
		if err != nil {
			return fmt.Errorf("failed to get or set election period for process %s: %w", p.Id, err)
		}
		keywords := pq.Array([]string{})
		if p.Sachgebiet != nil {
			keywords = pq.Array(*p.Sachgebiet)
		}
		date := sql.NullTime{Valid: false}
		if p.Datum != nil {
			date = sql.NullTime{Time: p.Datum.Time, Valid: true}
		}
		_, err = db.Exec("INSERT INTO processes (id, title, status, summary, keywords, election_period, type, date, api_updated) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)"+
			" ON CONFLICT (id) DO UPDATE SET title = $2, status = $3, summary = $4, keywords = $5, election_period = $6, type = $7, date = $8, api_updated = $9",
			p.Id, p.Titel, p.Beratungsstand, p.Abstract, keywords, electionPeriod, p.Vorgangstyp, date, p.Aktualisiert)
		if err != nil {
			return fmt.Errorf("failed to insert process %s: %w", p.Id, err)
		}
		if p.Initiative == nil {
			continue
		}
		for _, initiative := range *p.Initiative {
			groupId, err := getOrSetGroupByName(db, nil, &initiative, logger) //TODO: This can also be something like "Das Saarland", maybe we should not use "parliamentary_groups" for this?
			if err != nil {
				return fmt.Errorf("failed to get or set group for process %s: %w", p.Id, err)
			}
			_, err = db.Exec("INSERT INTO process_initiators (process_id, group_id) VALUES ($1, $2) ON CONFLICT (process_id, group_id) DO NOTHING", p.Id, groupId)
			if err != nil {
				return fmt.Errorf("failed to insert process initiator for process %s: %w", p.Id, err)
			}
		}
	}
	return nil
}

func ingestProcessPositions(client *dip.ClientWithResponses, lastSuccessTimestamp time.Time, currentTimestamp time.Time, db DBInterface, logger *Logger) error {
	logger.Debug("Ingesting process positions")
	var count = 0
	var cursor *string

	for {
		resp, err := client.GetVorgangspositionListWithResponse(context.Background(), &dip.GetVorgangspositionListParams{
			FAktualisiertStart: &lastSuccessTimestamp,
			FAktualisiertEnd:   &currentTimestamp,
			Cursor:             cursor,
		})
		if err != nil {
			return fmt.Errorf("failed to ingest process positions: %w", err)
		}
		if resp.JSON200 == nil {
			return fmt.Errorf("unexpected response status: %d", resp.StatusCode())
		}

		// Capture documents in closure for parallel ingestion
		documents := resp.JSON200.Documents

		if cursor != nil && *cursor == resp.JSON200.Cursor {
			break
		}

		ingestionWG.Add(1)
		ingestionsTasks <- func() {
			err := processProcessPositions(documents, db, logger)
			if err != nil {
				logger.Fatal(fmt.Sprintf("failed to process process positions: %v", err))
			}
		}
		count += len(documents)
		cursor = &resp.JSON200.Cursor
		logger.Debug(fmt.Sprintf("Got %d process positions with cursor %s from total %d", count, *cursor, resp.JSON200.NumFound))
		time.Sleep(requestTimeout)
	}
	return nil
}

func processProcessPositions(processPositions []dip.Vorgangsposition, db DBInterface, logger *Logger) error {
	logger.Debug(fmt.Sprintf("processing %d process positions", len(processPositions)))
	for _, p := range processPositions {
		documentType, err := getDocumentType(string(p.Dokumentart))
		if err != nil {
			return fmt.Errorf("failed to get document type for process position %s: %w", p.Id, err)
		}
		var printedPaperId *string = nil
		var protocolId *string = nil
		if documentType == DocumentPrintedPaper {
			printedPaperId = &p.Fundstelle.Id
			var exists bool
			err = db.Get(&exists, "SELECT EXISTS(SELECT 1 FROM printed_papers WHERE id=$1)", p.Fundstelle.Id)
			if err != nil {
				return fmt.Errorf("failed to check existence of printed paper %s: %w", p.Fundstelle.Id, err)
			}
			if !exists {
				logger.Warn(fmt.Sprintf("skipping process position %s: printed paper %s not found", p.Id, p.Fundstelle.Id))
				continue
			}
		} else {
			protocolId = &p.Fundstelle.Id
			var exists bool
			err = db.Get(&exists, "SELECT EXISTS(SELECT 1 FROM protocols WHERE id=$1)", p.Fundstelle.Id)
			if err != nil {
				return fmt.Errorf("failed to check existence of protocol %s: %w", p.Fundstelle.Id, err)
			}
			if !exists {
				logger.Warn(fmt.Sprintf("skipping process position %s: protocol %s not found", p.Id, p.Fundstelle.Id))
				continue
			}
		}
		processId := &p.VorgangId
		var exists bool
		err = db.Get(&exists, "SELECT EXISTS(SELECT 1 FROM processes WHERE id=$1)", *processId)
		if err != nil {
			return fmt.Errorf("failed to check existence of process %s: %w", *processId, err)
		}
		if !exists {
			logger.Warn(fmt.Sprintf("skipping process position %s: process %s not found", p.Id, *processId))
			continue
		}

		_, err = db.Exec("INSERT INTO process_positions (id, type, process_id, printed_paper_id, protocol_id, association, continuation, supplement, title, document_type, date, api_updated) "+
			"VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) "+
			"ON CONFLICT (id) DO UPDATE SET type = $2, process_id = $3, printed_paper_id = $4, protocol_id = $5, association = $6, continuation = $7, supplement = $8, title = $9, document_type = $10, date = $11, api_updated = $12",
			p.Id, p.Vorgangstyp, p.VorgangId, printedPaperId, protocolId, p.Zuordnung, p.Fortsetzung, p.Nachtrag, p.Titel, documentType, p.Datum.Time, p.Aktualisiert)
		if err != nil {
			// Check for foreign key violation (PostgreSQL code "23503")
			if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23503" {
				logger.Warn(fmt.Sprintf("foreign key constraint error inserting process position %s: %v, skipping", p.Id, err))
				continue
			}
			return fmt.Errorf("failed to insert process position %s: %w", p.Id, err)
		}
	}
	return nil
}

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
		//err = db.Select(&activities, "SELECT * FROM activities a WHERE protocol_id = $1 AND a.type = 'Rede' AND (text IS NULL OR text = '')", protocol.ID)
		err = db.Select(&activities, "SELECT * FROM activities a WHERE protocol_id = $1 AND a.type = 'Rede' AND (text IS NULL OR text = '') LIMIT 10", protocol.ID)
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
			texts, err := processSpeeches(speeches, speaker, activityIDs, logger)
			if err != nil {
				logger.Error(fmt.Sprintf("failed to process speeches: %v", err))
				return fmt.Errorf("failed to process speeches: %w", err)
			}
			for activityID, text := range texts {
				_, err = db.Exec("UPDATE activities SET text = $1 WHERE id = $2", text, activityID)
				if err != nil {
					logger.Error(fmt.Sprintf("failed to update activity text: %v", err))
					return fmt.Errorf("failed to update activity text: %w", err)
				}
			}
		}
	}
	return nil
}

func ingestData(reinitializeActivities bool, reinitializeEntities bool, reinitializeNewerThan *time.Time) error {
	reinitializeData := reinitializeActivities || reinitializeEntities
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	initIngestionWorker()

	// Logger uses db connection (not transaction) so logs always commit even if transaction rolls back
	consoleLogLevel := Debug
	logger := NewLogger(db, &consoleLogLevel, nil)

	logIngestionError := func(err error) {
		if err != nil {
			logger.Error(fmt.Sprintf("Failed to ingest data: %v", err))
			_, _ = db.Exec("INSERT INTO ingestion_logs (timestamp, status, error_message) VALUES (NOW(), 'failed', $1)", err.Error())
		}
	}

	if reinitializeNewerThan != nil {
		logger.Info(fmt.Sprintf("ingesting data. reinitialize activities: %t, reinitialize entities: %t, reinitialize newer than: %s", reinitializeActivities, reinitializeEntities, reinitializeNewerThan.Format("2006-01-02 15:04:05")))
	} else {
		logger.Info(fmt.Sprintf("ingesting data. reinitialize activities: %t, reinitialize entities: %t, reinitialize newer than not given", reinitializeActivities, reinitializeEntities))
	}

	// Begin transaction
	tx, err := db.Beginx()
	if err != nil {
		err = fmt.Errorf("failed to begin transaction: %w", err)
		logIngestionError(err)
		return err
	}
	var txErr error
	defer func() {
		if txErr != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				logger.Error(fmt.Sprintf("failed to rollback transaction: %v", rollbackErr))
			}
		}
	}()

	if reinitializeEntities {
		err = clearEntitiesDatabase(reinitializeNewerThan, tx, logger)
		if err != nil {
			err = fmt.Errorf("failed to clear entities database: %w", err)
			logIngestionError(err)
			return err
		}
	}
	if reinitializeActivities {
		err = clearActivitiesDatabase(reinitializeNewerThan, tx, logger)
		if err != nil {
			err = fmt.Errorf("failed to clear activities database: %w", err)
			logIngestionError(err)
			return err
		}
	}
	// Create a client with the DIP API server URL
	client, err := dip.NewClientWithResponses(
		"https://search.dip.bundestag.de/api/v1",
		dip.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			// Add API key via header (Authorization: ApiKey YOUR_KEY)
			req.Header.Set("Authorization", fmt.Sprintf("ApiKey %s", os.Getenv("DIP_API_Key")))
			return nil
		}),
	)
	if err != nil {
		err = fmt.Errorf("failed to create client: %w", err)
		logIngestionError(err)
		return err
	}

	var currentTimestamp = time.Now().UTC()
	lastSuccessTimestamp := time.Time{}

	if reinitializeData && reinitializeNewerThan != nil {
		lastSuccessTimestamp = *reinitializeNewerThan
		logger.Info(fmt.Sprintf("last success timestamp for reinitialization: %s", lastSuccessTimestamp))
	} else if !reinitializeData {
		lastSuccessTimestamp, err = getLastSuccessTimestamp(tx, logger)
		logger.Info(fmt.Sprintf("last success timestamp from database: %s", lastSuccessTimestamp))
		if err != nil {
			err = fmt.Errorf("failed to get last success timestamp: %w", err)
			logIngestionError(err)
			return err
		}
	}

	//------Ingestion Begins------//

	if reinitializeEntities || !reinitializeData {
		logger.Info("ingesting persons")

		err = ingestPersons(client, lastSuccessTimestamp, currentTimestamp, tx, logger)
		if err != nil {
			err = fmt.Errorf("failed to ingest persons: %w", err)
			logIngestionError(err)
			txErr = err
			return err
		}

		logger.Debug("Waiting for all person ingestion tasks to complete...")
		ingestionWG.Wait()
		logger.Info("All person ingestion tasks completed")

		time.Sleep(ingestionSleepTime)
	}

	if reinitializeActivities || !reinitializeData {

		logger.Info("ingesting protocols")

		err = ingestProtocols(client, lastSuccessTimestamp, currentTimestamp, tx, logger)
		if err != nil {
			err = fmt.Errorf("failed to ingest protocols: %w", err)
			logIngestionError(err)
			txErr = err
			return err
		}

		logger.Debug("Waiting for all protocol ingestion tasks to complete...")
		ingestionWG.Wait()
		logger.Info("All protocol ingestion tasks completed")

		time.Sleep(ingestionSleepTime)

		logger.Info("ingesting printed papers")

		err = ingestPrintedPapers(client, lastSuccessTimestamp, currentTimestamp, tx, logger)
		if err != nil {
			err = fmt.Errorf("failed to ingest printed papers: %w", err)
			logIngestionError(err)
			txErr = err
			return err
		}

		logger.Debug("Waiting for all printed paper ingestion tasks to complete...")
		ingestionWG.Wait()
		logger.Info("All printed paper ingestion tasks completed")

		time.Sleep(ingestionSleepTime)

		logger.Info("ingesting processes")

		err = ingestProcesses(client, lastSuccessTimestamp, currentTimestamp, tx, logger)
		if err != nil {
			err = fmt.Errorf("failed to ingest processes: %w", err)
			logIngestionError(err)
			txErr = err
			return err
		}

		logger.Debug("Waiting for all process ingestion tasks to complete...")
		ingestionWG.Wait()
		logger.Info("All process ingestion tasks completed")

		time.Sleep(ingestionSleepTime)

		logger.Info("ingesting process positions")

		err = ingestProcessPositions(client, lastSuccessTimestamp, currentTimestamp, tx, logger)
		if err != nil {
			err = fmt.Errorf("failed to ingest process positions: %w", err)
			logIngestionError(err)
			txErr = err
			return err
		}

		logger.Debug("Waiting for all process position ingestion tasks to complete...")
		ingestionWG.Wait()
		logger.Info("All process position ingestion tasks completed")

		time.Sleep(ingestionSleepTime)

		logger.Info("ingesting activities")

		err = ingestActivities(client, lastSuccessTimestamp, currentTimestamp, tx, logger)
		if err != nil {
			err = fmt.Errorf("failed to ingest activities: %w", err)
			logIngestionError(err)
			txErr = err
			return err
		}

		logger.Debug("Waiting for all activity ingestion tasks to complete...")
		ingestionWG.Wait()
		logger.Info("All activity ingestion tasks completed")
	}

	// Commit transaction
	if err = tx.Commit(); err != nil {
		err = fmt.Errorf("failed to commit transaction: %w", err)
		logIngestionError(err)
		txErr = err
		return err
	}

	_, err = db.Exec("INSERT INTO ingestion_logs (timestamp, status) VALUES ($1, 'success')", time.Now().UTC())
	if err != nil {
		err = fmt.Errorf("failed to insert ingestion log: %w", err)
		logIngestionError(err)
		txErr = err
		return err
	}

	logger.Info("Ingestion completed successfully")
	return nil
}
