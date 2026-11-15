-- Schema version 1. All timestamps are Unix milliseconds (UTC wall clock).

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

-- One row per process that owned the state store.
CREATE TABLE runs (
    run_id                 TEXT PRIMARY KEY,
    kind                   TEXT NOT NULL,           -- daemon, once, demo
    started_at             INTEGER NOT NULL,
    pid                    INTEGER NOT NULL,
    version                TEXT NOT NULL,
    config_revision        TEXT NOT NULL,
    mode                   TEXT NOT NULL,
    heartbeat_at           INTEGER NOT NULL,
    last_cycle_started_at  INTEGER,
    last_cycle_finished_at INTEGER,
    next_cycle_at          INTEGER,
    stopped_at             INTEGER,
    stop_reason            TEXT
) STRICT;

CREATE TABLE connections (
    id                   TEXT PRIMARY KEY,
    kind                 TEXT NOT NULL,
    name                 TEXT NOT NULL,
    base_url             TEXT NOT NULL,              -- sanitized
    status               TEXT NOT NULL DEFAULT 'unknown', -- unknown, ok, failing, disabled
    last_attempt_at      INTEGER,
    last_success_at      INTEGER,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    last_error_class     TEXT,
    last_error           TEXT,
    next_attempt_at      INTEGER,
    probe_json           TEXT,
    probed_at            INTEGER,
    health_json          TEXT,
    health_at            INTEGER,
    configured           INTEGER NOT NULL DEFAULT 1,
    updated_at           INTEGER NOT NULL
) STRICT;

CREATE TABLE connection_checks (
    id            INTEGER PRIMARY KEY,
    connection_id TEXT NOT NULL,
    at            INTEGER NOT NULL,
    op            TEXT NOT NULL,
    ok            INTEGER NOT NULL,
    error_class   TEXT,
    message       TEXT,
    duration_ms   INTEGER NOT NULL
) STRICT;
CREATE INDEX connection_checks_conn_at ON connection_checks(connection_id, at);
CREATE INDEX connection_checks_at ON connection_checks(at);

-- Latest known state of each physical transfer (client + native identity).
CREATE TABLE transfers (
    client_id       TEXT NOT NULL,
    native_id       TEXT NOT NULL,
    hash_v1         TEXT,
    hash_v2         TEXT,
    name            TEXT NOT NULL,
    state_class     TEXT NOT NULL,
    first_seen      INTEGER NOT NULL,
    last_seen       INTEGER NOT NULL,
    present         INTEGER NOT NULL,
    gone_at         INTEGER,
    latest_json     TEXT NOT NULL,
    assessment_json TEXT,
    assessed_at     INTEGER,
    PRIMARY KEY (client_id, native_id)
) STRICT;
CREATE INDEX transfers_hash_v1 ON transfers(hash_v1);
CREATE INDEX transfers_hash_v2 ON transfers(hash_v2);
CREATE INDEX transfers_present ON transfers(present, state_class);

-- Bounded time series for incomplete transfers.
CREATE TABLE transfer_observations (
    id          INTEGER PRIMARY KEY,
    client_id   TEXT NOT NULL,
    native_id   TEXT NOT NULL,
    at          INTEGER NOT NULL,
    run_id      TEXT NOT NULL,
    mono_ms     INTEGER NOT NULL,
    state_class TEXT NOT NULL,
    sample_json TEXT NOT NULL
) STRICT;
CREATE INDEX transfer_observations_key_at ON transfer_observations(client_id, native_id, at);
CREATE INDEX transfer_observations_at ON transfer_observations(at);

-- Latest known manager queue rows. item_key is derived from stable content
-- identity (download id + episode/movie id), never the transient row id.
CREATE TABLE queue_items (
    manager_id      TEXT NOT NULL,
    item_key        TEXT NOT NULL,
    queue_id        INTEGER,
    download_id     TEXT,
    first_seen      INTEGER NOT NULL,
    last_seen       INTEGER NOT NULL,
    present         INTEGER NOT NULL,
    gone_at         INTEGER,
    latest_json     TEXT NOT NULL,
    match_status    TEXT NOT NULL DEFAULT 'unknown',
    match_client_id TEXT,
    match_native_id TEXT,
    match_note      TEXT,
    attention_state TEXT,
    attention_since INTEGER,
    PRIMARY KEY (manager_id, item_key)
) STRICT;
CREATE INDEX queue_items_download ON queue_items(download_id);
CREATE INDEX queue_items_match ON queue_items(match_client_id, match_native_id);

CREATE TABLE findings (
    id                 INTEGER PRIMARY KEY,
    dedup_key          TEXT NOT NULL,
    type               TEXT NOT NULL,
    severity           TEXT NOT NULL,
    state              TEXT NOT NULL CHECK (state IN ('open', 'acknowledged', 'snoozed', 'resolved')),
    title              TEXT NOT NULL,
    summary            TEXT NOT NULL,
    subject_json       TEXT NOT NULL,
    detail_json        TEXT NOT NULL,
    policy_id          TEXT,
    policy_revision    TEXT,
    first_seen         INTEGER NOT NULL,
    last_observed      INTEGER NOT NULL,
    last_evaluated     INTEGER NOT NULL,
    evidence_at        INTEGER NOT NULL,
    next_evaluation_at INTEGER,
    acknowledged_at    INTEGER,
    snoozed_until      INTEGER,
    resolved_at        INTEGER,
    resolution         TEXT,
    previous_id        INTEGER REFERENCES findings(id) ON DELETE SET NULL,
    occurrence         INTEGER NOT NULL DEFAULT 1
) STRICT;
-- At most one unresolved finding per deduplication key.
CREATE UNIQUE INDEX findings_active_key ON findings(dedup_key) WHERE state != 'resolved';
CREATE INDEX findings_key ON findings(dedup_key, id);
CREATE INDEX findings_state ON findings(state, severity);
CREATE INDEX findings_resolved_at ON findings(resolved_at);

CREATE TABLE finding_events (
    id         INTEGER PRIMARY KEY,
    finding_id INTEGER NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
    at         INTEGER NOT NULL,
    kind       TEXT NOT NULL,
    actor      TEXT NOT NULL,
    detail     TEXT NOT NULL
) STRICT;
CREATE INDEX finding_events_finding ON finding_events(finding_id, at);
CREATE INDEX finding_events_at ON finding_events(at);

-- Mode/configuration changes and other operator-relevant events.
CREATE TABLE audit_events (
    id     INTEGER PRIMARY KEY,
    at     INTEGER NOT NULL,
    actor  TEXT NOT NULL,
    kind   TEXT NOT NULL,
    detail TEXT NOT NULL
) STRICT;
CREATE INDEX audit_events_at ON audit_events(at);
