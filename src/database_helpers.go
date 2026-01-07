package main

import (
	"database/sql"
	"fmt"
	"time"
)

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
