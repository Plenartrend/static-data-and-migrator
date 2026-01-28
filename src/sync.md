# Synchronization considerations
- Protocol processors cannot commit if a static-data-and-migrator ingestion is in progress, as that might lead to the static-data-and-migrator having to reset its work, which is much worse than aborting a single protocol processing step.

## requirements
- atomic lock "ingest_lock"

## static-data-and-migrator
- on start:
    - atomic lock "ingest_lock" with current timestamp if none present within the last hour
    - if returned id is not own: abort
- during execution:
    - periodically update timestamp of atomic lock "ingest_lock" if still owned
- on completion:
    - if we own the atomic lock "ingest_lock" and the time since last update is more less than an hour minus 5 minutes:
        - yes: commit and free atomic lock "ingest_lock"
        - no: abort


UUID = ...

UPDATE ingest_lock SET id = UUID, status = "in_progress", timestamp = NOW()
WHERE status != "in_progress" OR EXTRACT(EPOCH FROM (NOW() - timestamp)) < 3600
RETURNING id;


## protocol-processor
- on start:
    - wait until atomic lock "ingest_lock" is free and store current timestamp of atomic lock "ingest_lock"
- on completion:
    - check if atomic lock "ingest_lock" is free and timestamp is unchanged:
        - yes: commit
        - no: abort
