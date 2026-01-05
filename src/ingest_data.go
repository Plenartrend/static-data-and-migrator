package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	dip "plenartrend/static-data-and-migrator/src/openAPI"

	"github.com/jmoiron/sqlx"
)

// DBInterface allows using either *sqlx.DB or *sqlx.Tx
type DBInterface interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Get(dest interface{}, query string, args ...interface{}) error
	Select(dest interface{}, query string, args ...interface{}) error
	QueryRow(query string, args ...interface{}) *sql.Row
}

// setRole sets a role in the database and logs a warning if it already exists. This happens because of low quality data in the API.
func setRole(db DBInterface, personID string, name string, lastName string, firstName string, electionPeriod int, groupID *int, logger *Logger) error {
	var exists bool
	err := db.Get(&exists, `
		SELECT EXISTS(
			SELECT 1 FROM roles 
			WHERE person_id = $1 AND election_period = $2 AND name = $3
		)
	`, personID, electionPeriod, name)
	if err != nil {
		return fmt.Errorf("check role existence: %w", err)
	}

	if exists {
		logger.Info(fmt.Sprintf("skipping duplicate role for person %s, election period %d", personID, electionPeriod))
		return nil
	}

	_, err = db.Exec("INSERT INTO roles (person_id, name, last_name, first_name, election_period, group_id) VALUES ($1, $2, $3, $4, $5, $6)",
		personID, name, lastName, firstName, electionPeriod, groupID)
	if err != nil {
		return fmt.Errorf("insert role: %w", err)
	}
	return nil
}

func getLastSuccessTimestamp(db DBInterface, logger *Logger) (time.Time, error) {
	var lastSuccessTimestamp time.Time
	err := db.Get(&lastSuccessTimestamp, "SELECT l.timestamp FROM ingestion_logs l WHERE l.status = 'success' ORDER BY l.timestamp DESC LIMIT 1")
	if err == sql.ErrNoRows {
		lastSuccessTimestamp = time.Date(2026, 01, 01, 0, 0, 0, 0, time.UTC) //TODO: In prod this is emoty (minimal) date
	} else if err != nil {
		logger.Error(fmt.Sprintf("Failed to query last success timestamp: %v", err))
		return time.Time{}, fmt.Errorf("failed to query last success timestamp: %w", err)
	}
	return lastSuccessTimestamp, nil
}

func getAllPersons(client *dip.ClientWithResponses, lastSuccessTimestamp time.Time, currentTimestamp time.Time, logger *Logger) ([]dip.Person, error) {
	var persons = make(map[string]dip.Person)
	var cursor *string

	for {
		resp, err := client.GetPersonListWithResponse(context.Background(), &dip.GetPersonListParams{
			FAktualisiertStart: &lastSuccessTimestamp,
			FAktualisiertEnd:   &currentTimestamp,
			Cursor:             cursor,
		})
		if err != nil {
			logger.Error(fmt.Sprintf("failed to get person list: %v", err))
			return nil, fmt.Errorf("failed to get person list: %w", err)
		}
		if resp.JSON200 == nil {
			logger.Error(fmt.Sprintf("unexpected response: %v", resp))
			return nil, fmt.Errorf("unexpected response status: %d", resp.StatusCode())
		}

		for _, p := range resp.JSON200.Documents {
			persons[p.Id] = p
		}

		if int32(len(persons)) >= resp.JSON200.NumFound {
			break
		}
		cursor = &resp.JSON200.Cursor
	}
	return values(persons), nil
}

func getOrSetElectionPeriod(db DBInterface, number int, logger *Logger) (int, error) {
	var electionPeriod ElectionPeriod
	err := db.Get(&electionPeriod, "SELECT number, start_date, end_date FROM election_periods WHERE number = $1", number)
	if err == sql.ErrNoRows {
		var newNumber int
		err = db.QueryRow("INSERT INTO election_periods (number) VALUES ($1) RETURNING number", number).Scan(&newNumber)
		if err != nil {
			logger.Error(fmt.Sprintf("failed to insert election period: %v", err))
			return 0, fmt.Errorf("insert election period: %w", err)
		}
		return newNumber, nil
	} else if err != nil {
		logger.Error(fmt.Sprintf("failed to get election period: %v", err))
		return 0, fmt.Errorf("get election period: %w", err)
	}
	return electionPeriod.Number, nil
}

func getOrSetGroupByName(db DBInterface, shortName *string, name *string, logger *Logger) (int, error) {
	var group ParliamentaryGroup
	err := db.Get(&group, "SELECT id, name, short_name FROM parliamentary_groups WHERE short_name = $1 OR name = $2", shortName, name)

	if err == sql.ErrNoRows {
		var id int
		if err := db.QueryRow("INSERT INTO parliamentary_groups (name, short_name) VALUES ($1, $2) RETURNING id", name, shortName).Scan(&id); err != nil {
			logger.Error(fmt.Sprintf("failed to insert group: %v", err))
			return 0, fmt.Errorf("insert group: %w", err)
		}
		return id, nil
	}
	if err != nil {
		logger.Error(fmt.Sprintf("failed to get group: %v", err))
		return 0, fmt.Errorf("get group: %w", err)
	}

	// Update if either column is missing
	if (shortName != nil && !group.ShortName.Valid) || (name != nil && !group.Name.Valid) {
		newName := group.Name.String
		newShort := group.ShortName.String
		if name != nil && !group.Name.Valid {
			newName = *name
		}
		if shortName != nil && !group.ShortName.Valid {
			newShort = *shortName
		}
		if _, err := db.Exec("UPDATE parliamentary_groups SET name = $1, short_name = $2 WHERE id = $3", newName, newShort, group.ID); err != nil {
			logger.Error(fmt.Sprintf("failed to update group: %v", err))
			return 0, fmt.Errorf("update group: %w", err)
		}
	}
	return group.ID, nil
}

func ingestPersons(db DBInterface, persons []dip.Person, logger *Logger) error {
	for _, p := range persons {
		_, err := db.Exec("INSERT INTO persons (id, api_updated) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET api_updated = $2", p.Id, p.Aktualisiert)
		if err != nil {
			logger.Error(fmt.Sprintf("failed to insert person: %v", err))
			return fmt.Errorf("insert person %s: %w", p.Id, err)
		}

		// ----Validate required fields----
		if len(p.Funktion) != 1 {
			logger.Error(fmt.Sprintf("person %s: expected 1 Funktion, got %d", p.Id, len(p.Funktion)))
			return fmt.Errorf("person %s: expected 1 Funktion, got %d", p.Id, len(p.Funktion))
		}
		if p.Fraktion != nil && len(*p.Fraktion) > 1 {
			logger.Error(fmt.Sprintf("person %s: expected at most 1 Fraktion, got %d", p.Id, len(*p.Fraktion)))
			return fmt.Errorf("person %s: expected at most 1 Fraktion, got %d", p.Id, len(*p.Fraktion))
		}
		// ---End of validate required fields----

		var groupId *int
		if p.Fraktion != nil && len(*p.Fraktion) > 0 {
			fraktion := (*p.Fraktion)[0]
			id, err := getOrSetGroupByName(db, nil, &fraktion, logger)
			if err != nil {
				logger.Error(fmt.Sprintf("failed to get group for person %s: %v", p.Id, err))
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

func getProtocols(client *dip.ClientWithResponses, lastSuccessTimestamp time.Time, currentTimestamp time.Time) ([]dip.PlenarprotokollText, error) {
	var protocols = make([]dip.PlenarprotokollText, 0)
	var cursor *string

	for {
		resp, err := client.GetPlenarprotokollTextListWithResponse(context.Background(), &dip.GetPlenarprotokollTextListParams{
			FAktualisiertStart: &lastSuccessTimestamp,
			FAktualisiertEnd:   &currentTimestamp,
			Cursor:             cursor,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get protocol list: %w", err)
		}
		if resp.JSON200 == nil {
			return nil, fmt.Errorf("unexpected response status: %d", resp.StatusCode())
		}

		protocols = append(protocols, resp.JSON200.Documents...)

		if int32(len(protocols)) >= resp.JSON200.NumFound {
			break
		}
		cursor = &resp.JSON200.Cursor
	}

	return protocols, nil
}

func ingestProtocols(protocols []dip.PlenarprotokollText, db DBInterface, logger *Logger) error {
	for _, p := range protocols {
		var exists bool
		err := db.Get(&exists, "SELECT EXISTS(SELECT 1 FROM protocols WHERE id=$1)", p.Id)
		if err != nil {
			return fmt.Errorf("error checking existence of protocol %s: %w", p.Id, err)
		}

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

		is_present := p.Text != nil && *p.Text != "" // TODO do we really need this? We need to update it anyway in case anything has changed.

		if exists {
			_, err = db.Exec(`
				UPDATE protocols
				SET
				title=$2, document_number=$3, publisher=$4, session_note=$5, url=$6, text=$7, election_period=$8, date=$9,
				api_updated=$10, is_present=$11
				WHERE id=$1
			`, p.Id, p.Titel, p.Dokumentnummer, publisher, p.Sitzungsbemerkung, p.Fundstelle.PdfUrl, p.Text, electionPeriod, p.Datum.Time,
				p.Aktualisiert, is_present)
		} else {
			_, err = db.Exec(`
				INSERT
				INTO protocols
				(id, title, document_number, publisher, session_note, url, text, election_period, date, api_updated, is_present)
				VALUES
				($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
				ON CONFLICT (id) DO NOTHING
			`, p.Id, p.Titel, p.Dokumentnummer, publisher, p.Sitzungsbemerkung, p.Fundstelle.PdfUrl, p.Text, electionPeriod, p.Datum.Time,
				p.Aktualisiert, is_present)
		}

		if err != nil {
			return fmt.Errorf("failed to insert protocol %s: %w", p.Id, err)
		}
	}

	return nil
}

func getPrintedPapers(client *dip.ClientWithResponses, lastSuccessTimestamp time.Time, currentTimestamp time.Time) ([]dip.DrucksacheText, error) {
	var printedPapers = make([]dip.DrucksacheText, 0)
	var cursor *string

	for {
		resp, err := client.GetDrucksacheTextListWithResponse(context.Background(), &dip.GetDrucksacheTextListParams{
			FAktualisiertStart: &lastSuccessTimestamp,
			FAktualisiertEnd:   &currentTimestamp,
			Cursor:             cursor,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get printed paper list: %w", err)
		}
		if resp.JSON200 == nil {
			return nil, fmt.Errorf("unexpected response status: %d", resp.StatusCode())
		}

		printedPapers = append(printedPapers, resp.JSON200.Documents...)

		if int32(len(printedPapers)) >= resp.JSON200.NumFound {
			break
		}
		cursor = &resp.JSON200.Cursor
	}

	return printedPapers, nil
}

func ingestPrintedPapers(printedPapers []dip.DrucksacheText, db DBInterface, logger *Logger) error {
	for _, p := range printedPapers {
		var exists bool
		err := db.Get(&exists, "SELECT EXISTS(SELECT 1 FROM plenartrend.printed_papers WHERE id=$1)", p.Id)
		if err != nil {
			return fmt.Errorf("error checking existence of printed paper %s: %w", p.Id, err)
		}

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

		var passedDate sql.NullTime                          // TODO how do we get this information?
		var activeDate sql.NullTime                          // TODO how do we get this information?
		var is_present bool = p.Text != nil && *p.Text != "" // TODO do we really need this? We need to update it anyway in case anything has changed.

		if exists {
			_, err = db.Exec(`
				UPDATE printed_papers
				SET
				type=$2, title=$3, document_number=$4, publisher=$5, group_id=$6, url=$7, text=$8, election_period=$9, date=$10,
				api_updated=$11, passed_date=$12, active_date=$13, is_present=$14
				WHERE id=$1
			`, p.Id, p.Typ, p.Titel, p.Dokumentnummer, p.Herausgeber, groupId, p.Fundstelle.PdfUrl, p.Text, electionPeriod, p.Datum.Time,
				p.Aktualisiert, passedDate, activeDate, is_present)
		} else {
			_, err = db.Exec(`
				INSERT INTO printed_papers
				(id, type, title, document_number, publisher, group_id, url, text, election_period, date, api_updated, passed_date, active_date, is_present)
				VALUES
				($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
				ON CONFLICT (id) DO NOTHING
			`, p.Id, p.Typ, p.Titel, p.Dokumentnummer, p.Herausgeber, groupId, p.Fundstelle.PdfUrl, p.Text, electionPeriod, p.Datum.Time,
				p.Aktualisiert, passedDate, activeDate, is_present)
		}

		if err != nil {
			return fmt.Errorf("failed to insert printed paper %s: %w", p.Id, err)
		}

		_, err = db.Exec("DELETE FROM printed_paper_signers WHERE printed_paper_id=$1", p.Id)
		if err != nil {
			return fmt.Errorf("failed to delete printed paper signers for paper %s: %w", p.Id, err)
		}

		// TODO AutorenAnzeige does not contain all authors, we need to extract them from the text
		if p.AutorenAnzeige != nil {
			for _, author := range *p.AutorenAnzeige {
				var roleId int
				err := db.Get(&roleId, "SELECT id FROM roles WHERE person_id=$1 AND election_period=$2", author.Id, electionPeriod)
				if err != nil {
					return fmt.Errorf("failed to get role for printed paper author %s: %w", p.Id, err)
				}

				_, err = db.Exec(`
				INSERT INTO printed_paper_signers (printed_paper_id, role_id)
				VALUES ($1, $2)
				ON CONFLICT DO NOTHING
			`, p.Id, roleId)
				if err != nil {
					return fmt.Errorf("failed to insert printed paper author for paper %s: %w", p.Id, err)
				}
			}
		}
	}

	return nil
}

func getActivities(client *dip.ClientWithResponses, lastSuccessTimestamp time.Time, currentTimestamp time.Time) ([]dip.Aktivitaet, error) {
	var activities = make([]dip.Aktivitaet, 0)
	var cursor *string

	for {
		resp, err := client.GetAktivitaetListWithResponse(context.Background(), &dip.GetAktivitaetListParams{
			FAktualisiertStart: &lastSuccessTimestamp,
			FAktualisiertEnd:   &currentTimestamp,
			Cursor:             cursor,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get activity list: %w", err)
		}
		if resp.JSON200 == nil {
			return nil, fmt.Errorf("unexpected response status: %d", resp.StatusCode())
		}

		activities = append(activities, resp.JSON200.Documents...)

		if int32(len(activities)) >= resp.JSON200.NumFound {
			break
		}
		cursor = &resp.JSON200.Cursor
	}
	return activities, nil
}

func ingestActivities(activities []dip.Aktivitaet, db DBInterface, logger *Logger) error {
	for _, a := range activities {
		var exists bool
		err := db.Get(&exists, "SELECT EXISTS(SELECT 1 FROM plenartrend.activities WHERE id=$1)", a.Id)
		if err != nil {
			return fmt.Errorf("error checking existence of activity %s: %w", a.Id, err)
		}

		electionPeriod, err := getOrSetElectionPeriod(db, int(a.Wahlperiode), logger)
		if err != nil {
			return fmt.Errorf("error getting/setting election period for activity %s: %w", a.Id, err)
		}

		var roleId int
		err = db.Get(&roleId, `SELECT id from roles where person_id = $1 and election_period = $2`, a.PersonId, electionPeriod)
		if err != nil {
			return fmt.Errorf("error getting role of person %s in election period %d for activity %s: %w", a.PersonId, electionPeriod, a.Id, err)
		}

		documentTypeMap := map[dip.AktivitaetDokumentart]DocumentType{
			"Plenarprotokoll": DocumentProtocol,
			"Drucksache":      DocumentPrintedPaper,
		}
		documentType, ok := documentTypeMap[a.Dokumentart]
		if !ok {
			return fmt.Errorf("invalid document type for activity %s: %s", a.Id, a.Dokumentart)
		}

		printedPaperId := sql.NullInt32{Valid: false}
		protocolId := sql.NullInt32{Valid: false}

		if documentType == DocumentPrintedPaper {
			var ppId int32
			err = db.Get(&ppId, "SELECT id FROM printed_papers WHERE id=$1", a.Fundstelle.Id)
			if err != nil {
				return fmt.Errorf("error retrieving printed paper id %s for activity %s: %w", a.Fundstelle.Id, a.Id, err)
			}
			printedPaperId = sql.NullInt32{Int32: ppId, Valid: true}
		} else {
			var pId int32
			err = db.Get(&pId, "SELECT id FROM protocols WHERE id=$1", a.Fundstelle.Id)
			if err != nil {
				return fmt.Errorf("error retrieving protocol id %s for activity %s: %w", a.Fundstelle.Id, a.Id, err)
			}
			protocolId = sql.NullInt32{Int32: pId, Valid: true}
		}

		var text string = "" // TODO what is the text of the activity?

		if exists {
			_, err = db.Exec(`
				INSERT INTO activities
				(id, type, role_id, document_type, printed_paper_id, protocol_id, text, api_updated)
				VALUES
				($1, $2, $3, $4, $5, $6, $7, $8)
				ON CONFLICT (id) DO NOTHING
			`, a.Id, a.Typ, roleId, documentType, printedPaperId, protocolId, text, a.Aktualisiert)
		} else {
			_, err = db.Exec(`
				UPDATE activities
				SET
				type=$2, role_id=$3, document_type=$4, printed_paper_id=$5, protocol_id=$6, text=$7, api_updated=$8
				WHERE id=$1
			`, a.Id, a.Typ, roleId, documentType, printedPaperId, protocolId, text, a.Aktualisiert)
		}

		if err != nil {
			return fmt.Errorf("failed to insert activity %s: %w", a.Id, err)
		}
	}

	return nil
}

func clearActivitiesDatabase(reinitializeNewerThan *time.Time, db DBInterface, logger *Logger) error {
	// Delete in correct order (children first due to foreign keys)
	newerThanString := ""
	if reinitializeNewerThan != nil {
		newerThanString = fmt.Sprintf("WHERE api_updated >= '%s'", reinitializeNewerThan.Format("2006-01-02 15:04:05"))
	}
	if _, err := db.Exec(fmt.Sprintf("DELETE FROM protocols %s", newerThanString)); err != nil {
		logger.Error(fmt.Sprintf("failed to delete protocols: %v", err))
		return fmt.Errorf("delete protocols: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf("DELETE FROM printed_papers %s", newerThanString)); err != nil {
		logger.Error(fmt.Sprintf("failed to delete printed_papers: %v", err))
		return fmt.Errorf("delete printed_papers: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf("DELETE FROM activities %s", newerThanString)); err != nil {
		logger.Error(fmt.Sprintf("failed to delete activities: %v", err))
		return fmt.Errorf("delete activities: %w", err)
	}
	logger.Info("Cleared existing text data")
	return nil
}

func clearEntitiesDatabase(reinitializeNewerThan *time.Time, db DBInterface, logger *Logger) error {
	newerThanString := ""
	if reinitializeNewerThan != nil {
		newerThanString = fmt.Sprintf("WHERE api_updated >= '%s'", reinitializeNewerThan.Format("2006-01-02 15:04:05"))
	}
	if _, err := db.Exec(fmt.Sprintf("DELETE FROM persons %s", newerThanString)); err != nil {
		logger.Error(fmt.Sprintf("failed to delete persons: %v", err))
		return fmt.Errorf("delete persons: %w", err)
	}
	logger.Info("Cleared existing entities data")
	return nil
}

func ingestData(reinitializeActivities bool, reinitializeEntities bool, reinitializeNewerThan *time.Time) error {
	reinitializeData := reinitializeActivities || reinitializeEntities
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	// Logger uses db connection (not transaction) so logs always commit even if transaction rolls back
	logger := NewLogger(db, nil, nil)

	// Local helper to log errors to ingestion_logs
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
		logIngestionError(err)
		logger.Error(fmt.Sprintf("failed to begin transaction: %v", err))
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
			logIngestionError(err)
			logger.Error(fmt.Sprintf("failed to clear database: %v", err))
			return err
		}
	}
	if reinitializeActivities {
		err = clearActivitiesDatabase(reinitializeNewerThan, tx, logger)
		if err != nil {
			logIngestionError(err)
			logger.Error(fmt.Sprintf("failed to clear database: %v", err))
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
		logIngestionError(err)
		logger.Error(fmt.Sprintf("failed to create client: %v", err))
		return err
	}

	var currentTimestamp = time.Now().UTC()
	// Read from tx to see the deleted ingestion_logs (for testing fresh start)
	lastSuccessTimestamp := time.Time{}

	if reinitializeData && reinitializeNewerThan != nil {
		lastSuccessTimestamp = *reinitializeNewerThan
		logger.Info(fmt.Sprintf("last success timestamp for reinitialization: %s", lastSuccessTimestamp))
	} else if !reinitializeData {
		lastSuccessTimestamp, err = getLastSuccessTimestamp(tx, logger)
		logger.Info(fmt.Sprintf("last success timestamp from database: %s", lastSuccessTimestamp))
		if err != nil {
			logIngestionError(err)
			return err
		}
	}

	if reinitializeEntities || !reinitializeData {
		logger.Info(fmt.Sprintf("getting persons"))

		persons, err := getAllPersons(client, lastSuccessTimestamp, currentTimestamp, logger)
		if err != nil {
			logIngestionError(err)
			txErr = err
			return err
		}

		logger.Info(fmt.Sprintf("ingesting %d persons", len(persons)))

		err = ingestPersons(tx, persons, logger)
		if err != nil {
			logIngestionError(err)
			txErr = err
			return err
		}
		logger.Info(fmt.Sprintf("successfully ingested %d persons", len(persons)))
	}

	if reinitializeActivities || !reinitializeData {
		logger.Info(fmt.Sprintf("getting protocols"))

		protocols, err := getProtocols(client, lastSuccessTimestamp, currentTimestamp)
		if err != nil {
			logIngestionError(err)
			txErr = err
			return err
		}

		logger.Info(fmt.Sprintf("ingesting %d protocols", len(protocols)))

		err = ingestProtocols(protocols, tx, logger)
		if err != nil {
			logIngestionError(err)
			logger.Error(fmt.Sprintf("failed to ingest protocols: %v", err))
			txErr = err
			return err
		}
		logger.Info(fmt.Sprintf("successfully ingested %d protocols", len(protocols)))

		logger.Info(fmt.Sprintf("getting printed papers"))

		printedPapers, err := getPrintedPapers(client, lastSuccessTimestamp, currentTimestamp)
		if err != nil {
			logIngestionError(err)
			txErr = err
			return err
		}

		logger.Info(fmt.Sprintf("ingesting %d printed papers", len(printedPapers)))

		err = ingestPrintedPapers(printedPapers, tx, logger)
		if err != nil {
			logIngestionError(err)
			logger.Error(fmt.Sprintf("failed to ingest printed papers: %v", err))
			txErr = err
			return err
		}

		logger.Info(fmt.Sprintf("successfully ingested %d printed papers", len(printedPapers)))

		logger.Info(fmt.Sprintf("getting activities"))

		activities, err := getActivities(client, lastSuccessTimestamp, currentTimestamp)
		if err != nil {
			logIngestionError(err)
			txErr = err
			return err
		}

		logger.Info(fmt.Sprintf("ingesting %d activities", len(activities)))

		err = ingestActivities(activities, tx, logger)
		if err != nil {
			logIngestionError(err)
			logger.Error(fmt.Sprintf("failed to ingest activities: %v", err))
			txErr = err
			return err
		}
		logger.Info(fmt.Sprintf("successfully ingested %d activities", len(activities)))
	}

	_, err = tx.Exec("INSERT INTO ingestion_logs (timestamp, status) VALUES (NOW(), 'success')")
	if err != nil {
		logIngestionError(err)
		logger.Error(fmt.Sprintf("failed to insert ingestion log: %v", err))
		txErr = err
		return err
	}

	// Commit transaction
	if err = tx.Commit(); err != nil {
		logIngestionError(err)
		logger.Error(fmt.Sprintf("failed to commit transaction: %v", err))
		txErr = err
		return err
	}

	return nil
}
