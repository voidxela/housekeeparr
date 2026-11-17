-- Schema version 16: managers' search sources (indexers) and their status
-- (SPEC §9.2). Only names, protocol, automatic-search enablement and
-- failure times are stored: never URLs or credentials. ok_streak counts
-- consecutive reads in which the source was not disabled, so recovery is
-- established by more than one successful read.
CREATE TABLE source_status (
    manager_id      TEXT NOT NULL,
    source_id       INTEGER NOT NULL,
    name            TEXT NOT NULL,
    protocol        TEXT NOT NULL,
    automatic       INTEGER NOT NULL,
    disabled_until  INTEGER,
    last_failure    INTEGER,
    ok_streak       INTEGER NOT NULL DEFAULT 0,
    collected_at    INTEGER NOT NULL,
    PRIMARY KEY (manager_id, source_id)
);
