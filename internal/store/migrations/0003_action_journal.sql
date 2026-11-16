-- Schema version 3: durable action journal.
--
-- An intent is persisted, with its steps, locks, and budget reservation, in
-- one transaction before any external write. Each step is marked
-- 'dispatching' (committed) immediately before its request is sent, so a
-- crash leaves an explicit record that the outcome is unknown. Startup
-- reconciliation observes such steps; it never re-sends them.

CREATE TABLE action_intents (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    -- Local idempotency key: one intent per key. It prevents duplicate
    -- local execution; it cannot make an external API exactly-once.
    idem_key        TEXT NOT NULL UNIQUE,
    workflow        TEXT NOT NULL,
    origin          TEXT NOT NULL,            -- automatic | manual
    finding_id      INTEGER,
    subject_json    TEXT NOT NULL,
    -- proposed reserved executing reconciling succeeded failed uncertain cancelled
    state           TEXT NOT NULL,
    policy_revision TEXT NOT NULL,
    config_revision TEXT NOT NULL,
    instance_id     TEXT NOT NULL,
    -- Digest of decision-relevant external state when the intent was
    -- reserved; a re-fetch that differs cancels before dispatch.
    fingerprint     TEXT NOT NULL,
    evidence_json   TEXT NOT NULL,
    plan_json       TEXT NOT NULL,
    explanation     TEXT NOT NULL DEFAULT '',
    review_required INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    finished_at     INTEGER
);
CREATE INDEX action_intents_state ON action_intents(state);
CREATE INDEX action_intents_finding ON action_intents(finding_id);

CREATE TABLE action_steps (
    intent_id     INTEGER NOT NULL REFERENCES action_intents(id) ON DELETE CASCADE,
    seq           INTEGER NOT NULL,
    op            TEXT NOT NULL,
    target_json   TEXT NOT NULL,
    request_json  TEXT NOT NULL,              -- every consequential flag, explicit
    expect_json   TEXT NOT NULL,              -- expected postcondition
    -- pending dispatching succeeded failed uncertain skipped
    state         TEXT NOT NULL,
    attempts      INTEGER NOT NULL DEFAULT 0,
    dispatched_at INTEGER,
    finished_at   INTEGER,
    outcome_json  TEXT,
    error         TEXT,
    PRIMARY KEY (intent_id, seq)
);

-- Exclusive subjects (a transfer, a content item). A lock is held from
-- reservation until the intent ends; an uncertain intent keeps its locks so
-- conflicting work stays blocked until it is reviewed.
CREATE TABLE action_locks (
    subject     TEXT PRIMARY KEY,
    intent_id   INTEGER NOT NULL REFERENCES action_intents(id) ON DELETE CASCADE,
    acquired_at INTEGER NOT NULL
);
CREATE INDEX action_locks_intent ON action_locks(intent_id);

-- Persistent budgets: consumption survives restart.
CREATE TABLE action_budgets (
    scope        TEXT NOT NULL,
    window_start INTEGER NOT NULL,
    used         INTEGER NOT NULL,
    PRIMARY KEY (scope, window_start)
);

CREATE TABLE action_cooldowns (
    subject TEXT PRIMARY KEY,
    until   INTEGER NOT NULL,
    reason  TEXT NOT NULL
);
