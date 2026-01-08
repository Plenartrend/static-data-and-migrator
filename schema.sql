-- Schema for Plenartrend database
-- Generated from src/api_types.go

DROP SCHEMA IF EXISTS plenartrend CASCADE;
CREATE SCHEMA plenartrend;
SET SCHEMA 'plenartrend';
SET SEARCH_PATH TO plenartrend;

-- Enum types
CREATE TYPE document_type AS ENUM ('protocol', 'printedPaper');
CREATE TYPE body AS ENUM ('BT', 'BR', 'BV', 'EK');
CREATE TYPE ingestion_status AS ENUM ('success', 'failed');
CREATE TYPE log_status AS ENUM ('debug', 'info', 'warn', 'error', 'fatal');

-- Topics table
CREATE TABLE topics (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    updated TIMESTAMP,
    created TIMESTAMP
);

-- Parliamentary groups (Fraktionen)
CREATE TABLE parliamentary_groups (
    id SERIAL PRIMARY KEY,
    name TEXT,
    short_name TEXT,
    CONSTRAINT name_or_short_name_check CHECK (name IS NOT NULL OR short_name IS NOT NULL),
    updated TIMESTAMP,
    created TIMESTAMP
);

-- Election periods (Wahlperioden)
CREATE TABLE election_periods (
    number INTEGER PRIMARY KEY,
    start_date TIMESTAMP,
    end_date TIMESTAMP,
    updated TIMESTAMP,
    created TIMESTAMP
);

-- Persons
CREATE TABLE persons (
    id INTEGER PRIMARY KEY,
    api_updated TIMESTAMP,
    updated TIMESTAMP,
    created TIMESTAMP
);

-- Roles (connecting persons to parliamentary groups and election periods)
CREATE TABLE roles (
   id SERIAL PRIMARY KEY,
   name TEXT,
   academic_title TEXT,
   last_name TEXT NOT NULL,
   first_name TEXT NOT NULL,
   person_id INTEGER NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
   group_id INTEGER REFERENCES parliamentary_groups(id),
   election_period INTEGER REFERENCES election_periods(number),
   updated TIMESTAMP,
   created TIMESTAMP,
   UNIQUE (person_id, election_period, name) --Thus so far we cannot read changes of Fraktion within one period, as they have this same combination. But we need to check it, as some roles may appear double in the API.
);

-- Processes (Vorgänge)
CREATE TABLE processes (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    status TEXT,
    summary TEXT,
    keywords TEXT[],
    election_period INTEGER REFERENCES election_periods(number),
    type TEXT,
    date TIMESTAMP,
    api_updated TIMESTAMP,
    updated TIMESTAMP,
    created TIMESTAMP
);

CREATE TABLE process_initiators (
    process_id INTEGER NOT NULL REFERENCES processes(id) ON DELETE CASCADE,
    group_id INTEGER NOT NULL REFERENCES parliamentary_groups(id) ON DELETE CASCADE,
    updated TIMESTAMP,
    created TIMESTAMP,
    PRIMARY KEY (process_id, group_id)
);

-- Process-Topics junction table
CREATE TABLE process_topics (
    process_id INTEGER NOT NULL REFERENCES processes(id) ON DELETE CASCADE,
    topic_id INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    PRIMARY KEY (process_id, topic_id),
    updated TIMESTAMP,
    created TIMESTAMP
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
    api_updated TIMESTAMP,
    updated TIMESTAMP,
    created TIMESTAMP
);

-- Printed paper signers junction table
CREATE TABLE printed_paper_signers (
    printed_paper_id INTEGER NOT NULL REFERENCES printed_papers(id) ON DELETE CASCADE,
    role_id INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE, --Watch out when deleting roles: signer data will be lost!
    PRIMARY KEY (printed_paper_id, role_id),
    updated TIMESTAMP,
    created TIMESTAMP
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
    api_updated TIMESTAMP,
    updated TIMESTAMP,
    created TIMESTAMP
);

-- Activities
CREATE TABLE activities (
    id INTEGER PRIMARY KEY,
    type TEXT,
    role_id INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE, --Watch out when deleting roles: activity data will be lost!
    document_type document_type,
    printed_paper_id INTEGER REFERENCES printed_papers(id) ON DELETE CASCADE, --Watch out when deleting printed papers: activity data will be lost!
    protocol_id INTEGER REFERENCES protocols(id) ON DELETE CASCADE, --Watch out when deleting protocols: activity data will be lost!
    text TEXT,
    api_updated TIMESTAMP,
    updated TIMESTAMP,
    created TIMESTAMP,
    -- Ensure activity references either a printed paper or a protocol (or neither), but maintains consistency
    CONSTRAINT activity_document_check CHECK (
        (document_type = 'printedPaper' AND printed_paper_id IS NOT NULL AND protocol_id IS NULL) OR
        (document_type = 'protocol' AND protocol_id IS NOT NULL AND printed_paper_id IS NULL)
    )
);

-- Process positions
CREATE TABLE process_positions (
    id INTEGER PRIMARY KEY,
    type TEXT,
    process_id INTEGER NOT NULL REFERENCES processes(id) ON DELETE CASCADE,
    printed_paper_id INTEGER REFERENCES printed_papers(id) ON DELETE CASCADE,
    protocol_id INTEGER REFERENCES protocols(id) ON DELETE CASCADE,
    association body,
    continuation BOOLEAN DEFAULT FALSE,
    supplement BOOLEAN DEFAULT FALSE,
    title TEXT,
    document_type document_type,
    date TIMESTAMP,
    api_updated TIMESTAMP,
    updated TIMESTAMP,
    created TIMESTAMP
);

CREATE TABLE ingestion_logs (
    id SERIAL PRIMARY KEY,
    timestamp TIMESTAMP NOT NULL,
    status ingestion_status NOT NULL,
    error_message TEXT
);

CREATE TABLE logs (
    id SERIAL PRIMARY KEY,
    timestamp TIMESTAMP NOT NULL,
    status log_status NOT NULL,
    message TEXT NOT NULL
);

-- Trigger function to automatically set created and updated timestamps
CREATE OR REPLACE FUNCTION set_timestamps()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.created = CURRENT_TIMESTAMP;
        NEW.updated = CURRENT_TIMESTAMP;
    ELSIF TG_OP = 'UPDATE' THEN
        NEW.updated = CURRENT_TIMESTAMP;
        -- Preserve original created timestamp
        NEW.created = OLD.created;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Apply triggers to all tables with created/updated columns
CREATE TRIGGER set_timestamps_topics
    BEFORE INSERT OR UPDATE ON topics
    FOR EACH ROW EXECUTE FUNCTION set_timestamps();

CREATE TRIGGER set_timestamps_parliamentary_groups
    BEFORE INSERT OR UPDATE ON parliamentary_groups
    FOR EACH ROW EXECUTE FUNCTION set_timestamps();

CREATE TRIGGER set_timestamps_election_periods
    BEFORE INSERT OR UPDATE ON election_periods
    FOR EACH ROW EXECUTE FUNCTION set_timestamps();

CREATE TRIGGER set_timestamps_persons
    BEFORE INSERT OR UPDATE ON persons
    FOR EACH ROW EXECUTE FUNCTION set_timestamps();

CREATE TRIGGER set_timestamps_roles
    BEFORE INSERT OR UPDATE ON roles
    FOR EACH ROW EXECUTE FUNCTION set_timestamps();

CREATE TRIGGER set_timestamps_processes
    BEFORE INSERT OR UPDATE ON processes
    FOR EACH ROW EXECUTE FUNCTION set_timestamps();

CREATE TRIGGER set_timestamps_process_initiators
    BEFORE INSERT OR UPDATE ON process_initiators
    FOR EACH ROW EXECUTE FUNCTION set_timestamps();

CREATE TRIGGER set_timestamps_process_topics
    BEFORE INSERT OR UPDATE ON process_topics
    FOR EACH ROW EXECUTE FUNCTION set_timestamps();

CREATE TRIGGER set_timestamps_process_positions
    BEFORE INSERT OR UPDATE ON process_positions
    FOR EACH ROW EXECUTE FUNCTION set_timestamps();

CREATE TRIGGER set_timestamps_printed_papers
    BEFORE INSERT OR UPDATE ON printed_papers
    FOR EACH ROW EXECUTE FUNCTION set_timestamps();

CREATE TRIGGER set_timestamps_printed_paper_signers
    BEFORE INSERT OR UPDATE ON printed_paper_signers
    FOR EACH ROW EXECUTE FUNCTION set_timestamps();

CREATE TRIGGER set_timestamps_protocols
    BEFORE INSERT OR UPDATE ON protocols
    FOR EACH ROW EXECUTE FUNCTION set_timestamps();

CREATE TRIGGER set_timestamps_activities
    BEFORE INSERT OR UPDATE ON activities
    FOR EACH ROW EXECUTE FUNCTION set_timestamps();