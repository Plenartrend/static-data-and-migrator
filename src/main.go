package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

func main() {

	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Server healthy")
	})

	http.HandleFunc("/topics", func(w http.ResponseWriter, r *http.Request) {
		var topics []Topic
		err := db.Select(&topics, "SELECT * FROM plenartrend.topics")
		if err != nil {
			log.Fatalf("Failed to query topics: %v", err)
		}
		for _, topic := range topics {
			fmt.Println(topic)
		}
	})

	http.HandleFunc("/ingest", func(w http.ResponseWriter, r *http.Request) {
		reinitializeActivities := r.URL.Query().Get("reinitializeActivities") == "true"
		reinitializeEntities := r.URL.Query().Get("reinitializeEntities") == "true"
		reinitializeNewerThanPar := r.URL.Query().Get("reinitializeNewerThan")

		var reinitializeNewerThan *time.Time = nil
		var reinitParsed time.Time = time.Time{}

		if reinitializeNewerThanPar != "" {
			reinitParsed, err = time.Parse(time.RFC3339, reinitializeNewerThanPar)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "Failed to parse reinitializeNewerThan", err)
				return
			}
			reinitializeNewerThan = &reinitParsed
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "Failed to parse reinitializeNewerThan", err)
			return
		}
		err = ingestData(reinitializeActivities, reinitializeEntities, reinitializeNewerThan)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "Failed to ingest data")
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Data ingested successfully")
	})

	http.HandleFunc("/assign-speeches", func(w http.ResponseWriter, r *http.Request) {
		assignSpeechesToActivities()
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Speeches assigned to activities successfully")
	})

	http.HandleFunc("/test-gemini", func(w http.ResponseWriter, r *http.Request) {
		test()
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Test completed successfully")
	})

	log.Println("Server starting on :8080")
	http.ListenAndServe(":8080", nil)
}
