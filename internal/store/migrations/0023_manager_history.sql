-- Schema version 23: the managers' history, read incrementally, for the
-- release-source scorecard (grab -> import outcomes per indexer). One row
-- per manager history record; only the fields the scorecard uses, never
-- URLs. history_cursors records how far each manager's history has been
-- read contiguously, so reads resume and coverage is known.
CREATE TABLE manager_history (
    manager_id     TEXT    NOT NULL,
    event_id       INTEGER NOT NULL,
    event_type     TEXT    NOT NULL,
    at             INTEGER NOT NULL,
    download_id    TEXT,
    content_key    TEXT,
    source_title   TEXT,
    indexer        TEXT,
    release_group  TEXT,
    release_source TEXT,
    PRIMARY KEY (manager_id, event_id)
);
CREATE INDEX manager_history_download ON manager_history(manager_id, download_id);
CREATE INDEX manager_history_grabs ON manager_history(event_type, at);

CREATE TABLE history_cursors (
    manager_id     TEXT PRIMARY KEY,
    -- newest_id: every record up to it, back to covered_since, is stored.
    newest_id      INTEGER NOT NULL,
    covered_since  INTEGER,
    -- Backfill toward the horizon: the next record index to read and the
    -- history's total when it was computed (newer records shift indexes).
    backfill_index INTEGER NOT NULL DEFAULT 0,
    backfill_total INTEGER NOT NULL DEFAULT 0,
    complete       INTEGER NOT NULL DEFAULT 0,
    gap_at         INTEGER,
    updated_at     INTEGER NOT NULL
);
