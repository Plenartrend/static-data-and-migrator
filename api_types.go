package staticdataandmigrator

import (
	"database/sql"
	"time"
)

type DocumentType int

const (
	DocumentProtocol DocumentType = iota
	DocumentPrintedPaper
)

type Body string

const (
	Bundestag         Body = "BT"
	Bundesrat         Body = "BR"
	Bundesversammlung Body = "BV"
	Europakammer      Body = "EK"
)

type Topic struct {
	ID   int    `db:"id" json:"id,omitempty"`
	Name string `db:"name" json:"name,omitempty"`
}

type Process struct {
	ID             int       `db:"id" json:"id,omitempty"`
	Title          string    `db:"title" json:"title,omitempty"`
	Status         string    `db:"status" json:"status,omitempty"`
	GroupID        int       `db:"group_id" json:"group_id,omitempty"`
	Summary        string    `db:"summary" json:"summary,omitempty"`
	Keywords       []string  `db:"keywords" json:"keywords,omitempty"`
	ElectionPeriod int       `db:"election_period" json:"election_period,omitempty"`
	Type           string    `db:"type" json:"type,omitempty"`
	Date           time.Time `db:"date" json:"date,omitempty"`
	Updated        time.Time `db:"updated" json:"updated,omitempty"`
}

type ProcessTopics struct {
	ProcessID int `db:"process_id" json:"process_id,omitempty"`
	TopicID   int `db:"topic_id" json:"topic_id,omitempty"`
}

// Is this type necessary???
type ProcessPosition struct {
	ID           int          `db:"id" json:"id,omitempty"`
	Type         string       `db:"type" json:"type,omitempty"`
	ProcessID    int          `db:"process_id" json:"process_id,omitempty"`
	Association  Body         `db:"association" json:"association,omitempty"`
	Continuation bool         `db:"continuation" json:"continuation,omitempty"`
	Supplement   bool         `db:"supplement" json:"supplement,omitempty"`
	Title        string       `db:"title" json:"title,omitempty"`
	DocumentType DocumentType `db:"document_type" json:"document_type,omitempty"`
	Date         time.Time    `db:"date" json:"date,omitempty"`
	Updated      time.Time    `db:"updated" json:"updated,omitempty"`
}

type PrintedPaper struct {
	ID             int          `db:"id" json:"id,omitempty"`
	Type           string       `db:"type" json:"type,omitempty"`
	Title          string       `db:"title" json:"title,omitempty"`
	DocumentNumber string       `db:"document_number" json:"document_number,omitempty"`
	Publisher      Body         `db:"publisher" json:"publisher,omitempty"`
	GroupID        int          `db:"group_id" json:"group_id,omitempty"`
	URL            string       `db:"url" json:"url,omitempty"`
	Text           string       `db:"text" json:"text,omitempty"`
	ElectionPeriod int          `db:"election_period" json:"election_period,omitempty"`
	Date           time.Time    `db:"date" json:"date,omitempty"`
	Updated        time.Time    `db:"updated" json:"updated,omitempty"`
	PassedDate     sql.NullTime `db:"passed_date" json:"passed_date,omitempty"`
	ActiveDate     sql.NullTime `db:"active_date" json:"active_date,omitempty"`
	IsPresent      bool         `db:"is_present" json:"is_present,omitempty"`
}

type PrintedPaperSigner struct {
	PrintedPaperID int `db:"printed_paper_id" json:"printed_paper_id,omitempty"`
	RoleID         int `db:"role_id" json:"role_id,omitempty"`
}

type Protocol struct {
	ID             int       `db:"id" json:"id,omitempty"`
	Title          string    `db:"title" json:"title,omitempty"`
	DocumentNumber string    `db:"document_number" json:"document_number,omitempty"`
	Publisher      Body      `db:"publisher" json:"publisher,omitempty"`
	SessionNote    string    `db:"session_note" json:"session_note,omitempty"`
	URL            string    `db:"url" json:"url,omitempty"`
	Text           string    `db:"text" json:"text,omitempty"`
	ElectionPeriod int       `db:"election_period" json:"election_period,omitempty"`
	Date           time.Time `db:"date" json:"date,omitempty"`
	Updated        time.Time `db:"updated" json:"updated,omitempty"`
	IsPresent      bool      `db:"is_present" json:"is_present,omitempty"`
}

type Activity struct {
	ID             int           `db:"id" json:"id,omitempty"`
	Type           string        `db:"type" json:"type,omitempty"`
	RoleID         int           `db:"role_id" json:"role_id,omitempty"`
	DocumentType   DocumentType  `db:"document_type" json:"document_type,omitempty"`
	PrintedPaperID sql.NullInt64 `db:"printed_paper_id" json:"printed_paper_id,omitempty"`
	ProtocolID     sql.NullInt64 `db:"protocol_id" json:"protocol_id,omitempty"`
	Text           string        `db:"text" json:"text,omitempty"`
}

type Person struct {
	ID int `db:"id" json:"id,omitempty"`
}

type ParliamentaryGroup struct {
	ID        int    `db:"id" json:"id,omitempty"`
	Name      string `db:"name" json:"name,omitempty"`
	ShortName string `db:"short_name" json:"short_name,omitempty"`
}

type Role struct {
	ID             int    `db:"id" json:"id,omitempty"`
	RoleName       string `db:"name" json:"name,omitempty"`
	AcademicTitle  string `db:"academic_title" json:"academic_title,omitempty"`
	LastName       string `db:"last_name" json:"last_name,omitempty"`
	FirstName      string `db:"first_name" json:"first_name,omitempty"`
	PersonID       int    `db:"person_id" json:"person_id,omitempty"`
	GroupID        int    `db:"group_id" json:"group_id,omitempty"`
	ElectionPeriod int    `db:"election_period" json:"election_period,omitempty"`
}

type ElectionPeriod struct {
	ID        int       `db:"id" json:"id,omitempty"`
	Number    int       `db:"number" json:"number"`
	StartDate time.Time `db:"start_date" json:"start_date,omitempty"`
	EndDate   time.Time `db:"end_date" json:"end_date,omitempty"`
}

// TODO: Split protocol parts
