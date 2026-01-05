package main

import (
	"fmt"
	"log"

	"github.com/jmoiron/sqlx"
)

type LogStatus int

const (
	Debug LogStatus = iota
	Info
	Warn
	Error
	Fatal
)

type Logger struct {
	db               *sqlx.DB
	minConsoleLevel  LogStatus
	minDatabaseLevel LogStatus
}

func NewLogger(db *sqlx.DB, minConsoleLevel *LogStatus, minDatabaseLevel *LogStatus) *Logger {
	defaultLevel := Info
	if minConsoleLevel == nil {
		minConsoleLevel = &defaultLevel
	}
	if minDatabaseLevel == nil {
		minDatabaseLevel = &defaultLevel
	}
	return &Logger{db: db, minConsoleLevel: *minConsoleLevel, minDatabaseLevel: *minDatabaseLevel}
}

func (l *Logger) Debug(message string) {
	if l.minConsoleLevel <= Debug {
		fmt.Println("DEBUG: ", message)
	}
	if l.minDatabaseLevel <= Debug {
		l.Log("debug", message)
	}
}

func (l *Logger) Info(message string) {
	if l.minConsoleLevel <= Info {
		fmt.Println("INFO: ", message)
	}
	if l.minDatabaseLevel <= Info {
		l.Log("info", message)
	}
}

func (l *Logger) Warn(message string) {
	if l.minConsoleLevel <= Warn {
		fmt.Println("WARN: ", message)
	}
	if l.minDatabaseLevel <= Warn {
		l.Log("warn", message)
	}
}

func (l *Logger) Error(message string) {
	if l.minConsoleLevel <= Error {
		fmt.Println("ERROR: ", message)
	}
	if l.minDatabaseLevel <= Error {
		l.Log("error", message)
	}
}

func (l *Logger) Fatal(message string) {
	if l.minDatabaseLevel <= Fatal {
		l.Log("fatal", message)
	}
	if l.minConsoleLevel <= Fatal {
		fmt.Println("FATAL: ", message)
		panic(message)
	}
}

func (l *Logger) Log(status string, message string) {
	_, err := l.db.Exec("INSERT INTO logs (timestamp, status, message) VALUES (NOW(), $1, $2)", status, message)
	if err != nil {
		log.Printf("ERROR:Failed to write log to database: %v", err)
	}
}
