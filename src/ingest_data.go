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
		logger.Warn(fmt.Sprintf("skipping duplicate role for person %s, election period %d", personID, electionPeriod))
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
		lastSuccessTimestamp = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
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
		_, err := db.Exec("INSERT INTO persons (id) VALUES ($1)", p.Id)
		if err != nil {
			logger.Error(fmt.Sprintf("failed to insert person: %v", err))
			return fmt.Errorf("insert person %s: %w", p.Id, err)
		}

		// ----Validate required fields----
		if len(p.Funktion) == 0 || len(p.Funktion) > 1 {
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

func clearApiDatabase(db DBInterface, logger *Logger) error {
	// Delete in correct order (children first due to foreign keys)
	if _, err := db.Exec("DELETE FROM roles"); err != nil {
		logger.Error(fmt.Sprintf("failed to delete roles: %v", err))
		return fmt.Errorf("delete roles: %w", err)
	}
	if _, err := db.Exec("DELETE FROM persons"); err != nil {
		logger.Error(fmt.Sprintf("failed to delete persons: %v", err))
		return fmt.Errorf("delete persons: %w", err)
	}
	if _, err := db.Exec("DELETE FROM election_periods"); err != nil {
		logger.Error(fmt.Sprintf("failed to delete election_periods: %v", err))
		return fmt.Errorf("delete election_periods: %w", err)
	}
	if _, err := db.Exec("DELETE FROM parliamentary_groups"); err != nil {
		logger.Error(fmt.Sprintf("failed to delete parliamentary_groups: %v", err))
		return fmt.Errorf("delete parliamentary_groups: %w", err)
	}
	if _, err := db.Exec("DELETE FROM ingestion_logs"); err != nil {
		logger.Error(fmt.Sprintf("failed to delete ingestion_logs: %v", err))
		return fmt.Errorf("delete ingestion_logs: %w", err)
	}
	logger.Info("Cleared existing data")
	return nil
}

func ingestData(reinitializeDatabase bool) {
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
			_, _ = db.Exec("INSERT INTO ingestion_logs (timestamp, status, error_message) VALUES (NOW(), 'failed', $1)", err.Error())
		}
	}

	// Begin transaction
	tx, err := db.Beginx()
	if err != nil {
		logIngestionError(err)
		logger.Error(fmt.Sprintf("failed to begin transaction: %v", err))
		return
	}
	var txErr error
	defer func() {
		if txErr != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				logger.Error(fmt.Sprintf("failed to rollback transaction: %v", rollbackErr))
			}
		}
	}()

	if reinitializeDatabase {
		err = clearApiDatabase(tx, logger)
		if err != nil {
			logIngestionError(err)
			logger.Error(fmt.Sprintf("failed to clear database: %v", err))
			return
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
		return
	}

	var currentTimestamp = time.Now().UTC()
	// Read from tx to see the deleted ingestion_logs (for testing fresh start)
	lastSuccessTimestamp, err := getLastSuccessTimestamp(tx, logger)

	if err != nil {
		logIngestionError(err)
		return
	}

	logger.Info(fmt.Sprintf("last success timestamp: %s", lastSuccessTimestamp))

	persons, err := getAllPersons(client, lastSuccessTimestamp, currentTimestamp, logger)
	if err != nil {
		logIngestionError(err)
		txErr = err
		return
	}

	logger.Info(fmt.Sprintf("ingesting %d persons", len(persons)))

	err = ingestPersons(tx, persons, logger)
	if err != nil {
		logIngestionError(err)
		txErr = err
		return
	}

	_, err = tx.Exec("INSERT INTO ingestion_logs (timestamp, status) VALUES (NOW(), 'success')")
	if err != nil {
		logIngestionError(err)
		logger.Error(fmt.Sprintf("failed to insert ingestion log: %v", err))
		txErr = err
		return
	}

	// Commit transaction
	if err = tx.Commit(); err != nil {
		logIngestionError(err)
		logger.Error(fmt.Sprintf("failed to commit transaction: %v", err))
		txErr = err
		return
	}

	logger.Info(fmt.Sprintf("Successfully ingested %d persons", len(persons)))
}
