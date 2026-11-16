-- Schema version 12: operator pauses per workflow (SPEC §6 "workflow
-- pause"), distinct from disabling a workflow's automation in
-- configuration: a paused workflow sends nothing, automatic or manual,
-- until it is resumed. Pause and resume are audited.
CREATE TABLE workflow_pauses (
    workflow  TEXT PRIMARY KEY,
    paused_at INTEGER NOT NULL,
    actor     TEXT NOT NULL,
    note      TEXT NOT NULL
);
