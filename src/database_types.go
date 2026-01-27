package main

import (
	"database/sql"
	"slices"
	"time"
)

type DocumentType string

const (
	DocumentProtocol     DocumentType = "protocol"
	DocumentPrintedPaper DocumentType = "printedPaper"
)

type Body string

const (
	Bundestag         Body = "BT"
	Bundesrat         Body = "BR"
	Bundesversammlung Body = "BV"
	Europakammer      Body = "EK"
)

func IsValidBody(value string) bool {
	valid := []Body{Bundestag, Bundesrat, Bundesversammlung, Europakammer}
	return slices.Contains(valid, Body(value))
}

type Topic struct {
	ID      int       `db:"id" json:"id,omitempty"`
	Name    string    `db:"name" json:"name,omitempty"`
	Updated time.Time `db:"updated" json:"updated,omitempty"`
	Created time.Time `db:"created" json:"created,omitempty"`
}

type Process struct {
	ID             int       `db:"id" json:"id,omitempty"`
	Title          string    `db:"title" json:"title,omitempty"`
	Status         string    `db:"status" json:"status,omitempty"`
	Summary        string    `db:"summary" json:"summary,omitempty"`
	Keywords       []string  `db:"keywords" json:"keywords,omitempty"`
	ElectionPeriod int       `db:"election_period" json:"election_period,omitempty"`
	Type           string    `db:"type" json:"type,omitempty"`
	Date           time.Time `db:"date" json:"date,omitempty"`
	APIUpdated     time.Time `db:"api_updated" json:"api_updated,omitempty"`
	Updated        time.Time `db:"updated" json:"updated,omitempty"`
	Created        time.Time `db:"created" json:"created,omitempty"`
}

type ProcessTopics struct {
	ProcessID int       `db:"process_id" json:"process_id,omitempty"`
	TopicID   int       `db:"topic_id" json:"topic_id,omitempty"`
	Updated   time.Time `db:"updated" json:"updated,omitempty"`
	Created   time.Time `db:"created" json:"created,omitempty"`
}

type ProcessInitiator struct {
	ProcessID int       `db:"process_id" json:"process_id,omitempty"`
	GroupID   int       `db:"group_id" json:"group_id,omitempty"`
	Updated   time.Time `db:"updated" json:"updated,omitempty"`
	Created   time.Time `db:"created" json:"created,omitempty"`
}

// Is this type necessary???
type ProcessPosition struct {
	ID             int           `db:"id" json:"id,omitempty"`
	Type           string        `db:"type" json:"type,omitempty"`
	ProcessID      int           `db:"process_id" json:"process_id,omitempty"`
	PrintedPaperID sql.NullInt64 `db:"printed_paper_id" json:"printed_paper_id,omitempty"`
	ProtocolID     sql.NullInt64 `db:"protocol_id" json:"protocol_id,omitempty"`
	Association    Body          `db:"association" json:"association,omitempty"`
	Continuation   bool          `db:"continuation" json:"continuation,omitempty"`
	Supplement     bool          `db:"supplement" json:"supplement,omitempty"`
	Title          string        `db:"title" json:"title,omitempty"`
	DocumentType   DocumentType  `db:"document_type" json:"document_type,omitempty"`
	Date           time.Time     `db:"date" json:"date,omitempty"`
	APIUpdated     time.Time     `db:"api_updated" json:"api_updated,omitempty"`
	Updated        time.Time     `db:"updated" json:"updated,omitempty"`
	Created        time.Time     `db:"created" json:"created,omitempty"`
}

type PrintedPaper struct {
	ID             int           `db:"id" json:"id,omitempty"`
	Type           string        `db:"type" json:"type,omitempty"`
	Title          string        `db:"title" json:"title,omitempty"`
	DocumentNumber string        `db:"document_number" json:"document_number,omitempty"`
	Publisher      Body          `db:"publisher" json:"publisher,omitempty"`
	GroupID        sql.NullInt64 `db:"group_id" json:"group_id,omitempty"`
	URL            string        `db:"url" json:"url,omitempty"`
	Text           string        `db:"text" json:"text,omitempty"`
	ElectionPeriod int           `db:"election_period" json:"election_period,omitempty"`
	Date           time.Time     `db:"date" json:"date,omitempty"`
	APIUpdated     time.Time     `db:"api_updated" json:"api_updated,omitempty"`
	Updated        time.Time     `db:"updated" json:"updated,omitempty"`
	Created        time.Time     `db:"created" json:"created,omitempty"`
}

type Protocol struct {
	ID                  int              `db:"id" json:"id,omitempty"`
	Title               string           `db:"title" json:"title,omitempty"`
	DocumentNumber      string           `db:"document_number" json:"document_number,omitempty"`
	Publisher           Body             `db:"publisher" json:"publisher,omitempty"`
	SessionNote         sql.NullString   `db:"session_note" json:"session_note,omitempty"`
	URL                 string           `db:"url" json:"url,omitempty"`
	Text                string           `db:"text" json:"text,omitempty"`
	ElectionPeriod      int              `db:"election_period" json:"election_period,omitempty"`
	Date                time.Time        `db:"date" json:"date,omitempty"`
	APIUpdated          time.Time        `db:"api_updated" json:"api_updated,omitempty"`
	Updated             time.Time        `db:"updated" json:"updated,omitempty"`
	Created             time.Time        `db:"created" json:"created,omitempty"`
	ProcessingStatus    ProcessingStatus `db:"processing_status" json:"processing_status,omitempty"`
	AttemptsCount       int              `db:"attempts_count" json:"attempts_count,omitempty"`
	ProcessingTimestamp sql.NullTime     `db:"processing_timestamp" json:"processing_timestamp,omitempty"`
}

type Activity struct {
	ID             int           `db:"id" json:"id,omitempty"`
	Type           string        `db:"type" json:"type,omitempty"`
	RoleID         int           `db:"role_id" json:"role_id,omitempty"`
	DocumentType   DocumentType  `db:"document_type" json:"document_type,omitempty"`
	PrintedPaperID sql.NullInt64 `db:"printed_paper_id" json:"printed_paper_id,omitempty"`
	ProtocolID     sql.NullInt64 `db:"protocol_id" json:"protocol_id,omitempty"`
	Text           string        `db:"text" json:"text,omitempty"`
	APIUpdated     time.Time     `db:"api_updated" json:"api_updated,omitempty"`
	Updated        time.Time     `db:"updated" json:"updated,omitempty"`
	Created        time.Time     `db:"created" json:"created,omitempty"`
}

type Person struct {
	ID         int       `db:"id" json:"id,omitempty"`
	APIUpdated time.Time `db:"api_updated" json:"api_updated,omitempty"`
	Updated    time.Time `db:"updated" json:"updated,omitempty"`
	Created    time.Time `db:"created" json:"created,omitempty"`
}

type ParliamentaryGroup struct {
	ID        int            `db:"id" json:"id,omitempty"`
	Name      sql.NullString `db:"name" json:"name,omitempty"`
	ShortName sql.NullString `db:"short_name" json:"short_name,omitempty"`
	Updated   time.Time      `db:"updated" json:"updated,omitempty"`
	Created   time.Time      `db:"created" json:"created,omitempty"`
}

type Role struct {
	ID             int            `db:"id" json:"id,omitempty"`
	RoleName       sql.NullString `db:"name" json:"name,omitempty"`
	Title          sql.NullString `db:"title" json:"title,omitempty"`
	NameSuffix     sql.NullString `db:"name_suffix" json:"name_suffix,omitempty"`
	LastName       string         `db:"last_name" json:"last_name,omitempty"`
	FirstName      string         `db:"first_name" json:"first_name,omitempty"`
	PersonID       int            `db:"person_id" json:"person_id,omitempty"`
	GroupID        sql.NullInt64  `db:"group_id" json:"group_id,omitempty"`
	ElectionPeriod sql.NullInt64  `db:"election_period" json:"election_period,omitempty"`
	Updated        time.Time      `db:"updated" json:"updated,omitempty"`
	Created        time.Time      `db:"created" json:"created,omitempty"`
}

type ElectionPeriod struct {
	Number    int          `db:"number" json:"number"`
	StartDate sql.NullTime `db:"start_date" json:"start_date,omitempty"`
	EndDate   sql.NullTime `db:"end_date" json:"end_date,omitempty"`
	Updated   time.Time    `db:"updated" json:"updated,omitempty"`
	Created   time.Time    `db:"created" json:"created,omitempty"`
}

type IngestionStatus string

const (
	IngestionStatusInProgress IngestionStatus = "in_progress"
	IngestionStatusSuccess    IngestionStatus = "success"
	IngestionStatusFailed     IngestionStatus = "failed"
)

/*
type IngestionStep int

const (
	IngestionStepPersons IngestionStep = iota
	IngestionStepProtocols
	IngestionStepPrintedPapers
	IngestionStepProcesses
	IngestionStepProcessPositions
	IngestionStepActivities
)

var ingestionStepStrings = map[IngestionStep]string{
	IngestionStepPersons:          "persons",
	IngestionStepProtocols:        "protocols",
	IngestionStepPrintedPapers:    "printed_papers",
	IngestionStepProcesses:        "processes",
	IngestionStepProcessPositions: "process_positions",
	IngestionStepActivities:       "activities",
}

func (s IngestionStep) String() string {
	return ingestionStepStrings[s]
}

func ParseIngestionStep(s string) (IngestionStep, bool) {
	for step, name := range ingestionStepStrings {
		if name == s {
			return step, true
		}
	}
	return 0, false
}
*/

type IngestionStep string

const (
	IngestionStepPersons          IngestionStep = "persons"
	IngestionStepProtocols        IngestionStep = "protocols"
	IngestionStepPrintedPapers    IngestionStep = "printed_papers"
	IngestionStepProcesses        IngestionStep = "processes"
	IngestionStepProcessPositions IngestionStep = "process_positions"
	IngestionStepActivities       IngestionStep = "activities"
)

func (s IngestionStep) Next() (IngestionStep, bool) {
	switch s {
	case IngestionStepPersons:
		return IngestionStepProtocols, true
	case IngestionStepProtocols:
		return IngestionStepPrintedPapers, true
	case IngestionStepPrintedPapers:
		return IngestionStepProcesses, true
	case IngestionStepProcesses:
		return IngestionStepProcessPositions, true
	case IngestionStepProcessPositions:
		return IngestionStepActivities, true
	default:
		return "", false
	}
}

type ProcessingStatus string

const (
	ProcessingStatusNotStarted ProcessingStatus = "not_started"
	ProcessingStatusInProgress ProcessingStatus = "in_progress"
	ProcessingStatusCompleted  ProcessingStatus = "completed"
	ProcessingStatusFailed     ProcessingStatus = "failed"
)

type IngestionLog struct {
	ID           int             `db:"id" json:"id,omitempty"`
	IngestFrom   time.Time       `db:"ingest_from" json:"ingest_from,omitempty"`
	IngestTo     time.Time       `db:"ingest_to" json:"ingest_to,omitempty"`
	Status       IngestionStatus `db:"status" json:"status,omitempty"`
	Step         IngestionStep   `db:"step" json:"step,omitempty"`
	ErrorMessage sql.NullString  `db:"error_message" json:"error_message,omitempty"`
	Created      time.Time       `db:"created" json:"created,omitempty"`
	Updated      time.Time       `db:"updated" json:"updated,omitempty"`
}

// TODO: Split protocol parts
