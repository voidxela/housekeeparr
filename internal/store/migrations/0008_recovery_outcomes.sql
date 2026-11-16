-- Schema version 8: what happened after a verified recovery (SPEC §7 step
-- 6). Each outcome is observed separately from the action's own steps:
-- a replacement grab (a new queue row for the same content with a
-- different download ID), its download progress, and completion. Absence
-- of a grab is reported as "not observed", never as proof that no release
-- exists. Tracking ends after a bounded window.
CREATE TABLE recovery_outcomes (
    intent_id        INTEGER PRIMARY KEY,
    manager_id       TEXT NOT NULL,
    content          TEXT NOT NULL,
    old_download_id  TEXT NOT NULL,
    started_at       INTEGER NOT NULL,
    state            TEXT NOT NULL,
    grabbed_at       INTEGER,
    new_download_id  TEXT,
    new_client_id    TEXT,
    new_native_id    TEXT,
    grab_verified    INTEGER,
    progress_at      INTEGER,
    completed_at     INTEGER,
    checked_at       INTEGER NOT NULL,
    note             TEXT NOT NULL DEFAULT ''
);
CREATE INDEX recovery_outcomes_state ON recovery_outcomes(state);
