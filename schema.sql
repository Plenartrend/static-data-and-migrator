-- Schema for Plenartrend database
-- Generated from src/api_types.go

DROP SCHEMA IF EXISTS plenartrend CASCADE;
CREATE SCHEMA plenartrend;
SET SCHEMA 'plenartrend';

-- Enum types
CREATE TYPE document_type AS ENUM ('protocol', 'printedPaper');
CREATE TYPE body AS ENUM ('BT', 'BR', 'BV', 'EK');

-- Topics table
CREATE TABLE topics (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL
);

-- Parliamentary groups (Fraktionen)
CREATE TABLE parliamentary_groups (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    short_name TEXT NOT NULL
);

-- Election periods (Wahlperioden)
CREATE TABLE election_periods (
    number INTEGER PRIMARY KEY,
    start_date TIMESTAMP NOT NULL,
    end_date TIMESTAMP
);

-- Persons
CREATE TABLE persons (
    id INTEGER PRIMARY KEY
);

-- Roles (connecting persons to parliamentary groups and election periods)
CREATE TABLE roles (
    id SERIAL PRIMARY KEY,
    name TEXT,
    academic_title TEXT,
    last_name TEXT NOT NULL,
    first_name TEXT NOT NULL,
    person_id INTEGER NOT NULL REFERENCES persons(id),
    group_id INTEGER REFERENCES parliamentary_groups(id),
    election_period INTEGER REFERENCES election_periods(number)
);

-- Processes (Vorgänge)
CREATE TABLE processes (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    status TEXT,
    group_id INTEGER REFERENCES parliamentary_groups(id),
    summary TEXT,
    keywords TEXT[],
    election_period INTEGER REFERENCES election_periods(number),
    type TEXT,
    date TIMESTAMP,
    updated TIMESTAMP
);

-- Process-Topics junction table
CREATE TABLE process_topics (
    process_id INTEGER NOT NULL REFERENCES processes(id) ON DELETE CASCADE,
    topic_id INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    PRIMARY KEY (process_id, topic_id)
);

-- Process positions
CREATE TABLE process_positions (
    id INTEGER PRIMARY KEY,
    type TEXT,
    process_id INTEGER NOT NULL REFERENCES processes(id) ON DELETE CASCADE,
    association body,
    continuation BOOLEAN DEFAULT FALSE,
    supplement BOOLEAN DEFAULT FALSE,
    title TEXT,
    document_type document_type,
    date TIMESTAMP,
    updated TIMESTAMP
);

-- Printed papers (Drucksachen)
CREATE TABLE printed_papers (
    id INTEGER PRIMARY KEY,
    type TEXT,
    title TEXT NOT NULL,
    document_number TEXT NOT NULL,
    publisher body,
    group_id INTEGER REFERENCES parliamentary_groups(id),
    url TEXT,
    text TEXT,
    election_period INTEGER REFERENCES election_periods(number),
    date TIMESTAMP,
    updated TIMESTAMP,
    passed_date TIMESTAMP,
    active_date TIMESTAMP,
    is_present BOOLEAN DEFAULT FALSE
);

-- Printed paper signers junction table
CREATE TABLE printed_paper_signers (
    printed_paper_id INTEGER NOT NULL REFERENCES printed_papers(id) ON DELETE CASCADE,
    role_id INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    PRIMARY KEY (printed_paper_id, role_id)
);

-- Protocols (Plenarprotokolle)
CREATE TABLE protocols (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    document_number TEXT NOT NULL,
    publisher body,
    session_note TEXT,
    url TEXT,
    text TEXT,
    election_period INTEGER REFERENCES election_periods(number),
    date TIMESTAMP,
    updated TIMESTAMP,
    is_present BOOLEAN DEFAULT FALSE
);

-- Activities
CREATE TABLE activities (
    id INTEGER PRIMARY KEY,
    type TEXT,
    role_id INTEGER NOT NULL REFERENCES roles(id),
    document_type document_type,
    printed_paper_id INTEGER REFERENCES printed_papers(id),
    protocol_id INTEGER REFERENCES protocols(id),
    text TEXT,
    -- Ensure activity references either a printed paper or a protocol (or neither), but maintains consistency
    CONSTRAINT activity_document_check CHECK (
        (document_type = 'printedPaper' AND printed_paper_id IS NOT NULL AND protocol_id IS NULL) OR
        (document_type = 'protocol' AND protocol_id IS NOT NULL AND printed_paper_id IS NULL)
    )
);