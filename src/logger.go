package main

import (
	"fmt"
	"log"

	"github.com/jmoiron/sqlx"
)

type Logger struct {
	db              *sqlx.DB
	minConsoleLevel LogStatus
	storeDebug      bool
}

func NewLogger(db *sqlx.DB, minLevel LogStatus) *Logger {
	return &Logger{db: db, minConsoleLevel: minLevel}
}

func (l *Logger) Debug(message string) {
	if l.minConsoleLevel <= Debug {
		fmt.Println("Debug: ", message)
	}
	if l.storeDebug {
		l.Log(Debug, message)
	}
}

func (l *Logger) Info(message string) {
	if l.minConsoleLevel <= Info {
		fmt.Println("Info: ", message)
	}
	l.Log(Info, message)
}

func (l *Logger) Warn(message string) {
	if l.minConsoleLevel <= Warn {
		fmt.Println("Warn: ", message)
	}
	l.Log(Warn, message)
}

func (l *Logger) Error(message string) {
	log.Println("Error: ", message)
	l.Log(Error, message)
}

func (l *Logger) Fatal(message string) {
	l.Log(Fatal, message)
	log.Fatal("Fatal: ", message)
}

func (l *Logger) Log(status LogStatus, message string) {
	_, err := l.db.Exec("INSERT INTO logs (timestamp, status, message) VALUES (NOW(), $1, $2)", status, message)
	if err != nil {
		log.Printf("Failed to write log to database: %v", err)
	}
}
