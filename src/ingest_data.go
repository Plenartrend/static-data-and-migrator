package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	dip "plenartrend/static-data-and-migrator/src/openAPI"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

const defaultRequestTimeout = 100 * time.Millisecond

var requestTimeout = defaultRequestTimeout

const ingestionSleepTime = 1 * time.Second

const ingestProgressLogEvery = 1000

func shouldLogIngestProgress(prevCount, newCount int) bool {
	// Log on first iteration (first page) and whenever the running count crosses
	// a new 1000 boundary (1000, 2000, 3000, ...).
	if prevCount == 0 {
		return true
	}
	return newCount/ingestProgressLogEvery > prevCount/ingestProgressLogEvery
}

// DBInterface allows using either *sqlx.DB or *sqlx.Tx
type DBInterface interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Get(dest interface{}, query string, args ...interface{}) error
	Select(dest interface{}, query string, args ...interface{}) error
	QueryRow(query string, args ...interface{}) *sql.Row
}

type IngestionStepFunc func(*dip.ClientWithResponses, time.Time, time.Time, DBInterface, *Logger) error

var ingestionWG sync.WaitGroup
var ingestionsTasks = make(chan func())
var workerOnce sync.Once
var processingError error

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

func heartbeatWorker(db *sqlx.DB, logger *Logger) {
	for {
		_, err := db.Exec("UPDATE ingestion_lock SET heartbeat = CURRENT_TIMESTAMP WHERE id = 1")
		if err != nil {
			logger.Fatal(fmt.Sprintf("heartbeat update failed: %v", err))
		} else {
			logger.Debug("heartbeat update succeeded")
		}

		time.Sleep(1 * time.Minute)
	}
}

func ingestPersons(client *dip.ClientWithResponses, lastSuccessTimestamp time.Time, currentTimestamp time.Time, db DBInterface, logger *Logger) error {
	logger.Debug("Ingesting persons")
	var count = 0
	var cursor *string

	for {
		beforePersonrequestTimestamp := time.Now().UTC()
		resp, err := client.GetPersonListWithResponse(context.Background(), &dip.GetPersonListParams{
			FAktualisiertStart: &lastSuccessTimestamp,
			FAktualisiertEnd:   &currentTimestamp,
			//FDatumStart:        &dip.DatumStartFilter{Time: INGEST_ACTIVITIES_START_DATE},
			FDatumEnd: &dip.DatumEndFilter{Time: currentTimestamp},
			Cursor:    cursor,
		})
		logger.Debug(fmt.Sprintf("Person request took %v", time.Since(beforePersonrequestTimestamp)))
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
				if processingError == nil {
					processingError = err
				}
			}
		}

		prevCount := count
		count += len(documents)
		cursor = &resp.JSON200.Cursor
		msg := fmt.Sprintf("Got %d persons with cursor %s from total %d", count, *cursor, resp.JSON200.NumFound)
		logger.Debug(msg)
		if shouldLogIngestProgress(prevCount, count) {
			logger.Info(msg)
		}
		time.Sleep(requestTimeout)
	}

	return nil
}

func getElectionPeriodsForNewestRole(mainElectionPeriods []int32, roleElectionPeriods []int32) []int32 {
	var result []int32
	var maxPeriod int32 = -1
	for _, period := range mainElectionPeriods {
		if period > maxPeriod {
			maxPeriod = period
		}
		if !slices.Contains(roleElectionPeriods, period) {
			result = append(result, period)
		}
	}
	if maxPeriod != -1 && !slices.Contains(result, maxPeriod) {
		result = append(result, maxPeriod)
	}
	return result
}

func getRoleElectionPeriods(roles *[]dip.PersonRole) []int32 {
	var result []int32
	if roles == nil {
		return result
	}
	for _, role := range *roles {
		if role.WahlperiodeNummer != nil && len(*role.WahlperiodeNummer) > 0 {
			for _, period := range *role.WahlperiodeNummer {
				if !slices.Contains(result, period) {
					result = append(result, period)
				}
			}
		}
	}
	return result
}

func processPersons(db DBInterface, persons []dip.Person, logger *Logger) error {
	for _, p := range persons {
		_, err := db.Exec(`
			INSERT INTO persons
			(id, api_updated)
			VALUES ($1, $2)
			ON CONFLICT (id) DO NOTHING
		`, p.Id, p.Aktualisiert)
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

		wahlperioden = getElectionPeriodsForNewestRole(wahlperioden, getRoleElectionPeriods(p.PersonRoles))

		for _, wp := range wahlperioden {
			electionPeriod, err := getOrSetElectionPeriod(db, int(wp), logger)
			if err != nil {
				return fmt.Errorf("get election period for person %s: %w", p.Id, err)
			}

			if err := setRole(db, p.Id, p.Funktion[0], p.Nachname, p.Vorname, &p.Titel, p.Namenszusatz, electionPeriod, groupId, logger); err != nil {
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
					if err := setRole(db, p.Id, r.Funktion, r.Nachname, r.Vorname, nil, r.Namenszusatz, electionPeriod, groupId, logger); err != nil {
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
			FDatumStart:        &dip.DatumStartFilter{Time: INGEST_ACTIVITIES_START_DATE},
			FDatumEnd:          &dip.DatumEndFilter{Time: currentTimestamp},
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
				if processingError == nil {
					processingError = err
				}
			}
		}

		prevCount := count
		count += len(documents)
		cursor = &resp.JSON200.Cursor
		msg := fmt.Sprintf("Got %d protocols with cursor %s from total %d", count, *cursor, resp.JSON200.NumFound)
		logger.Debug(msg)
		if shouldLogIngestProgress(prevCount, count) {
			logger.Info(msg)
		}
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

		exists := true

		var existingProtocol Protocol
		err := db.Get(&existingProtocol, "SELECT * FROM protocols WHERE id = $1", p.Id)

		if err == sql.ErrNoRows {
			exists = false
		} else if err != nil {
			return fmt.Errorf("check protocol existence: %w", err)
		}

		if exists {
			if len(existingProtocol.Text) <= 1000 {
				_, err = db.Exec(`
					UPDATE protocols SET
						text = $2,
						api_updated = $3
					WHERE id = $1
				`, p.Id, sanitizeStringPtr(p.Text), p.Aktualisiert)
			}
		} else {
			_, err = db.Exec(`
				INSERT INTO protocols
				(id, title, document_number, publisher, session_note, url, text, election_period, date, api_updated)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			`, p.Id, p.Titel, p.Dokumentnummer, publisher, p.Sitzungsbemerkung, p.Fundstelle.PdfUrl, sanitizeStringPtr(p.Text), electionPeriod, p.Datum.Time,
				p.Aktualisiert)
		}

		if err != nil {
			return fmt.Errorf("failed to insert or update protocol %s: %w", p.Id, err)
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
			FDatumStart:        &dip.DatumStartFilter{Time: INGEST_ACTIVITIES_START_DATE},
			FDatumEnd:          &dip.DatumEndFilter{Time: currentTimestamp},
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
				if processingError == nil {
					processingError = err
				}
			}
		}

		prevCount := count
		count += len(documents)
		cursor = &resp.JSON200.Cursor
		msg := fmt.Sprintf("Got %d printed papers with cursor %s from total %d", count, *cursor, resp.JSON200.NumFound)
		logger.Debug(msg)
		if shouldLogIngestProgress(prevCount, count) {
			logger.Info(msg)
		}
		time.Sleep(requestTimeout)
	}

	return nil
}

func processPrintedPapers(printedPapers []dip.DrucksacheText, db DBInterface, logger *Logger) error {
	for _, p := range printedPapers {
		if p.Wahlperiode == nil {
			logger.Warn(fmt.Sprintf("printed paper %s has no Wahlperiode, skipping", p.Id))
			continue
		}

		electionPeriod, err := getOrSetElectionPeriod(db, int(*p.Wahlperiode), logger)
		if err != nil {
			return fmt.Errorf("error getting/setting election period for protocol %s: %w", p.Id, err)
		}

		var groupId *int
		if len(p.Fundstelle.Urheber) == 0 {
			logger.Debug(fmt.Sprintf("printed paper %s has no Urheber", p.Id))
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

		exists := true

		var existingPrintedPaper PrintedPaper
		err = db.Get(&existingPrintedPaper, "SELECT * FROM printed_papers WHERE id = $1", p.Id)

		if err == sql.ErrNoRows {
			exists = false
		} else if err != nil {
			return fmt.Errorf("check printed paper existence: %w", err)
		}

		if exists {
			if len(existingPrintedPaper.Text) <= 1000 {
				_, err = db.Exec(`
					UPDATE printed_papers SET
						text = $2,
						api_updated = $3
					WHERE id = $1
				`, p.Id, sanitizeStringPtr(p.Text), p.Aktualisiert)
			}
			continue
		} else {
			_, err = db.Exec(`
				INSERT INTO printed_papers
				(id, type, title, document_number, publisher, group_id, url, text, election_period, date, api_updated)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			`, p.Id, p.Drucksachetyp, p.Titel, p.Dokumentnummer, p.Herausgeber, groupId, p.Fundstelle.PdfUrl, sanitizeStringPtr(p.Text), electionPeriod, p.Datum.Time,
				p.Aktualisiert)
		}

		if err != nil {
			return fmt.Errorf("failed to insert or update printed paper %s: %w", p.Id, err)
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
			FDatumStart:        &dip.DatumStartFilter{Time: INGEST_ACTIVITIES_START_DATE},
			FDatumEnd:          &dip.DatumEndFilter{Time: currentTimestamp},
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
				if processingError == nil {
					processingError = err
				}
			}
		}

		prevCount := count
		count += len(documents)
		cursor = &resp.JSON200.Cursor
		msg := fmt.Sprintf("Got %d activities with cursor %s from total %d", count, *cursor, resp.JSON200.NumFound)
		logger.Debug(msg)
		if shouldLogIngestProgress(prevCount, count) {
			logger.Info(msg)
		}
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

		var text string = "" // Will be added during processing of protocols/printed papers

		_, err = db.Exec(`
			INSERT INTO activities
			(id, type, role_id, document_type, printed_paper_id, protocol_id, text, api_updated)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (id) DO NOTHING
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
			FDatumStart:        &dip.DatumStartFilter{Time: INGEST_ACTIVITIES_START_DATE},
			FDatumEnd:          &dip.DatumEndFilter{Time: currentTimestamp},
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
				logger.Error(fmt.Sprintf("failed to process processes: %v", err))
				if processingError == nil {
					processingError = err
				}
			}
		}
		prevCount := count
		count += len(documents)
		cursor = &resp.JSON200.Cursor
		msg := fmt.Sprintf("Got %d processes with cursor %s from total %d", count, *cursor, resp.JSON200.NumFound)
		logger.Debug(msg)
		if shouldLogIngestProgress(prevCount, count) {
			logger.Info(msg)
		}
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
		_, err = db.Exec(`
			INSERT INTO processes
			(id, title, status, summary, keywords, election_period, type, date, api_updated)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (id) DO NOTHING
		`, p.Id, p.Titel, p.Beratungsstand, p.Abstract, keywords, electionPeriod, p.Vorgangstyp, date, p.Aktualisiert)
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
			_, err = db.Exec(`
				INSERT INTO process_initiators
				(process_id, group_id)
				VALUES ($1, $2)
				ON CONFLICT (process_id, group_id) DO NOTHING
			`, p.Id, groupId)
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
			FDatumStart:        &dip.DatumStartFilter{Time: INGEST_ACTIVITIES_START_DATE},
			FDatumEnd:          &dip.DatumEndFilter{Time: currentTimestamp},
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
				logger.Error(fmt.Sprintf("failed to process process positions: %v", err))
				if processingError == nil {
					processingError = err
				}
			}
		}
		prevCount := count
		count += len(documents)
		cursor = &resp.JSON200.Cursor
		msg := fmt.Sprintf("Got %d process positions with cursor %s from total %d", count, *cursor, resp.JSON200.NumFound)
		logger.Debug(msg)
		if shouldLogIngestProgress(prevCount, count) {
			logger.Info(msg)
		}
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

		_, err = db.Exec(`
			INSERT INTO process_positions
			(id, type, process_id, printed_paper_id, protocol_id, association, continuation, supplement, title, document_type, date, api_updated)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (id) DO NOTHING
		`, p.Id, p.Vorgangstyp, p.VorgangId, printedPaperId, protocolId, p.Zuordnung, p.Fortsetzung, p.Nachtrag, p.Titel, documentType, p.Datum.Time, p.Aktualisiert)
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

// TODO do we really want to do this in a transaction?
func clearDB(db *sqlx.DB, logger *Logger) error {
	tx, err := db.Beginx()
	if err != nil {
		return fmt.Errorf("failed to begin clear transaction: %w", err)
	}

	err = clearEntitiesDatabase(nil, tx, logger)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to clear entities database: %w", err)
	}

	err = clearActivitiesDatabase(nil, tx, logger)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to clear activities database: %w", err)
	}

	if err = tx.Commit(); err != nil {
		err = fmt.Errorf("failed to commit clear transaction: %w", err)
		return err
	}

	logger.Info("Database cleared")
	return nil
}

func updateRequestTimeout(db *sqlx.DB, logger *Logger) error {
	var status IngestionStatus
	err := db.Get(&status, "SELECT status FROM ingestion_logs ORDER BY updated DESC LIMIT 1")
	if err == sql.ErrNoRows {
		logger.Info("No ingestion logs found, resetting request timeout to default")
		requestTimeout = defaultRequestTimeout
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get last ingestion status: %w", err)
	}
	if status == IngestionStatusSuccess {
		logger.Info("Last ingestion was successful, resetting request timeout to default")
		requestTimeout = defaultRequestTimeout
	} else {
		logger.Info("Last ingestion was not successful, increasing request timeout")
		requestTimeout = defaultRequestTimeout * 2
	}
	return nil
}

func ingestStep(client dip.ClientWithResponses, step IngestionStep, stepFunc IngestionStepFunc, from time.Time, to time.Time, db *sqlx.DB, logger *Logger) error {
	logger.Info(fmt.Sprintf("Ingesting %s", step))
	err := updateRequestTimeout(db, logger)
	if err != nil {
		logger.Error(fmt.Sprintf("failed to update request timeout: %v", err))
		return err
	}

	logIngestionError := func(err error, ingestionLogID int) {
		if err != nil {
			logger.Error(fmt.Sprintf("Failed to ingest data: %v", err))
			_, _ = db.Exec("UPDATE ingestion_logs SET status = 'failed', error_message = $1 WHERE id = $2", err.Error(), ingestionLogID)
		}
	}

	var logId int
	err = db.Get(&logId, "INSERT INTO ingestion_logs (ingest_from, ingest_to, status, step, error_message) VALUES ($1, $2, 'in_progress', $3, NULL) RETURNING id", from, to, step)
	if err != nil {
		logger.Error(fmt.Sprintf("failed to create ingestion log: %v", err))
		return err
	}

	tx, err := db.Beginx()
	if err != nil {
		err = fmt.Errorf("failed to begin transaction for %s: %w", step, err)
		logIngestionError(err, logId)
		return err
	}

	defer tx.Rollback()

	err = stepFunc(&client, from, to, tx, logger)
	if err != nil {
		err = fmt.Errorf("failed to ingest %s: %w", step, err)
		logIngestionError(err, logId)
		return err
	}

	logger.Debug(fmt.Sprintf("Waiting for all %s ingestion tasks to complete...", step))
	ingestionWG.Wait()

	if processingError != nil {
		logger.Error(fmt.Sprintf("processingError: %v", processingError))
		logIngestionError(processingError, logId)
		return fmt.Errorf("processingError: %w", processingError)
	}

	_, err = tx.Exec("UPDATE ingestion_logs SET status = 'success', step=$1 , ingest_from = $2, ingest_to = $3 WHERE id = $4", step, from, to, logId)
	if err != nil {
		err = fmt.Errorf("failed to update ingestion log to success: %w", err)
		logIngestionError(err, logId)
		return err
	}
	logger.Info(fmt.Sprintf("All %s ingestion tasks completed, committing transaction...", step))

	if err = tx.Commit(); err != nil {
		err = fmt.Errorf("failed to commit transaction for %s: %w", step, err)
		logIngestionError(err, logId)
		return err
	}

	logger.Info(fmt.Sprintf("Completed step: %s", step))
	return nil
}

func ingestData(db *sqlx.DB, initializeNewerThan time.Time, reinitialize bool) error {
	// Logger uses db connection (not transaction) so logs always commit even if transaction rolls back
	logger := NewLogger(db, &logLevel, &logLevel, serviceLogPrefix)
	processingError = nil

	var lastStep IngestionStep
	err := db.Get(&lastStep, "SELECT step FROM ingestion_logs WHERE status = 'success' ORDER BY updated DESC LIMIT 1")

	if err != nil && err != sql.ErrNoRows {
		logger.Error(fmt.Sprintf("failed to query in-progress ingestion: %v", err))
		return err
	}

	_, ok := lastStep.Next() // Only returns true if there is a next step, also works with empty lastStep
	if ok {
		reinitialize = false
	}

	if reinitialize {
		logger.Info("Clearing database")
		err := clearDB(db, logger)
		if err != nil {
			logger.Error(fmt.Sprintf("failed to clear database: %v", err))
			return fmt.Errorf("failed to clear database: %w", err)
		}
	}

	client, err := dip.NewClientWithResponses(
		"https://search.dip.bundestag.de/api/v1",
		dip.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			// Add API key via header (Authorization: ApiKey YOUR_KEY)
			req.Header.Set("Authorization", fmt.Sprintf("ApiKey %s", os.Getenv("DIP_API_Key")))
			return nil
		}),
	)
	if err != nil {
		logger.Error(fmt.Sprintf("failed to create DIP client: %v", err))
		return fmt.Errorf("failed to create DIP client: %w", err)
	}

	//------Ingestion Begins------//

	stepMap := map[IngestionStep]IngestionStepFunc{
		IngestionStepPersons:          ingestPersons,
		IngestionStepProtocols:        ingestProtocols,
		IngestionStepPrintedPapers:    ingestPrintedPapers,
		IngestionStepProcesses:        ingestProcesses,
		IngestionStepProcessPositions: ingestProcessPositions,
		IngestionStepActivities:       ingestActivities,
	}

	nextStep, ok := lastStep.Next()

	if !ok {
		nextStep = IngestionStepPersons
		ok = true
	}

	var startTimestamp = time.Now().UTC()

	fromPersons, to, err := getNextIngestPeriod(db, logger, time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		logger.Error(fmt.Sprintf("failed to get next ingest period for persons: %v", err))
		return fmt.Errorf("failed to get next ingest period for persons: %w", err)
	}
	from, _, err := getNextIngestPeriod(db, logger, initializeNewerThan)
	if err != nil {
		logger.Error(fmt.Sprintf("failed to get next ingest period: %v", err))
		return fmt.Errorf("failed to get next ingest period: %w", err)
	}
	logger.Info(fmt.Sprintf("Ingesting persons from %s to %s and all other steps from %s to %s", fromPersons, to, from, to))

	for ok {
		logger.Info(fmt.Sprintf("Starting next ingestion step: %s", nextStep))
		if nextStep == IngestionStepPersons {
			err = ingestStep(*client, nextStep, stepMap[nextStep], fromPersons, to, db, logger)
		} else {
			err = ingestStep(*client, nextStep, stepMap[nextStep], from, to, db, logger)
		}
		if err != nil {
			logger.Error(fmt.Sprintf("ingestion step %s failed: %v", nextStep, err))
			return fmt.Errorf("ingestion step %s failed: %w", nextStep, err)
		}
		logger.Info(fmt.Sprintf("Finished ingestion step: %s", nextStep))

		nextStep, ok = IngestionStep(nextStep).Next()
		time.Sleep(ingestionSleepTime)
	}
	logger.Info(fmt.Sprintf("Ingestion completed successfully within %v", time.Since(startTimestamp)))
	return nil
}
