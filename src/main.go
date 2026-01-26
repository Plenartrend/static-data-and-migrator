package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

type ActivitiesTexts struct {
	Activities []int
	Texts      []string
	Speaker    string
	Protocol   *Protocol
}

var ingestionWorkerRunning = false

func getDateOrDefault(dateStr string, defaultTime time.Time) (time.Time, error) {
	if dateStr == "" {
		return defaultTime, nil
	}
	parsedDate, err := time.Parse(time.RFC3339, dateStr)
	return parsedDate, err
}

var INGEST_ACTIVITIES_START_DATE = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
var INGESTION_SLEEP_DURATION = 1 * time.Hour

func buildDatabaseURL() (string, error) {
	requiredVars := map[string]string{
		"DATABASE_USER":     os.Getenv("DATABASE_USER"),
		"DATABASE_PASSWORD": os.Getenv("DATABASE_PASSWORD"),
		"DATABASE_HOST":     os.Getenv("DATABASE_HOST"),
		"DATABASE_PORT":     os.Getenv("DATABASE_PORT"),
		"DATABASE_NAME":     os.Getenv("DATABASE_NAME"),
	}

	var missingVars []string
	for varName, varValue := range requiredVars {
		if varValue == "" {
			missingVars = append(missingVars, varName)
		}
	}

	if len(missingVars) > 0 {
		return "", fmt.Errorf("missing required environment variables: %s", strings.Join(missingVars, ", "))
	}

	user := url.QueryEscape(requiredVars["DATABASE_USER"])
	password := url.QueryEscape(requiredVars["DATABASE_PASSWORD"])
	host := requiredVars["DATABASE_HOST"]
	port := requiredVars["DATABASE_PORT"]
	dbname := requiredVars["DATABASE_NAME"]

	databaseURL := fmt.Sprintf("postgres://%s:%s@%s:%s/%s", user, password, host, port, dbname)

	sslmode := os.Getenv("DATABASE_SSLMODE")
	if sslmode != "" {
		databaseURL += "?sslmode=" + url.QueryEscape(sslmode)
	}

	return databaseURL, nil
}

func runIngestionLoop(db *sqlx.DB, logger *Logger) {
	for {
		if !ingestionWorkerRunning {
			time.Sleep(1 * time.Second)
			continue
		}

		logger.Info("Starting ingestion cycle")

		err := ingestData(db, INGEST_ACTIVITIES_START_DATE, false)
		if err != nil {
			logger.Error(fmt.Sprintf("Failed to ingest data: %v", err))
		} else {
			logger.Info("Ingestion cycle completed successfully")
		}

		logger.Info(fmt.Sprintf("Sleeping for %v before next ingestion cycle", INGESTION_SLEEP_DURATION))
		time.Sleep(INGESTION_SLEEP_DURATION)
	}
}

func main() {
	_ = godotenv.Load()

	databaseURL, err := buildDatabaseURL()
	if err != nil {
		log.Fatalf("Database configuration error: %v", err)
	}

	var db *sqlx.DB
	for true {
		db, err = sqlx.Connect("postgres", databaseURL)
		if err == nil {
			defer db.Close()
			break
		}
		log.Printf("Failed to connect to database: %v", err)
		time.Sleep(time.Second)
	}

	initIngestionWorker()

	consoleLogLevel := Debug
	logger := NewLogger(db, &consoleLogLevel, nil)

	INGEST_ACTIVITIES_START_DATE, err = getDateOrDefault(os.Getenv("INGEST_ACTIVITIES_START_DATE"), INGEST_ACTIVITIES_START_DATE)
	if err != nil {
		logger.Error(fmt.Sprintf("failed to parse INGEST_ACTIVITIES_START_DATE: %v", err))
		return
	}

	if sleepEnv := os.Getenv("INGESTION_INTERVAL_MINUTES"); sleepEnv != "" {
		if minutes, err := strconv.Atoi(sleepEnv); err == nil {
			INGESTION_SLEEP_DURATION = time.Duration(minutes) * time.Minute
		} else {
			logger.Error(fmt.Sprintf("failed to parse INGESTION_INTERVAL_MINUTES: %v", err))
			return
		}
	}

	ingestionWorkerRunning = os.Getenv("BEGIN_INGESTION_ON_STARTUP") == "true"

	go runIngestionLoop(db, logger)

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Server healthy")
	})

	http.HandleFunc("/control-ingestion", func(w http.ResponseWriter, r *http.Request) {
		ingestionWorkerRunning = r.URL.Query().Get("start") == "true"
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Ingestion status updated with start="+strconv.FormatBool(ingestionWorkerRunning))
	})

	http.HandleFunc("/reinitialize", func(w http.ResponseWriter, r *http.Request) {
		reinitializeStartDatePar := r.URL.Query().Get("reinitializeStartDate")

		reinitializeStartDate, err := getDateOrDefault(reinitializeStartDatePar, time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "Failed to parse reinitializeStartDate", err)
			return
		}

		err = ingestData(db, reinitializeStartDate, true)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "Failed to ingest data")
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Data ingested successfully")
	})

	log.Println("Server starting on :8080")
	http.ListenAndServe(":8080", nil)
}
