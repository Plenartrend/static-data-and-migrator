package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

func main() {
	db, err := sqlx.Connect("postgres", "host=postgres user=Postgres password=Postgres dbname=Plenartrend sslmode=disable") // TODO: Use environment variables
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

	fmt.Println("Server starting on :8080")
	http.ListenAndServe(":8080", nil)
}
