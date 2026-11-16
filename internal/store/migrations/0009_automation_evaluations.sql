-- Schema version 9: the latest automatic evaluation of each finding, so
-- automation's decision (dispatched, deferred until a time, held by a
-- circuit breaker, refused by a live check, needing review, at a ceiling)
-- stays visible instead of being recomputed silently each cycle.
CREATE TABLE automation_evaluations (
    finding_id   INTEGER PRIMARY KEY,
    evaluated_at INTEGER NOT NULL,
    result       TEXT NOT NULL,
    detail       TEXT NOT NULL,
    next_at      INTEGER,
    intent_id    INTEGER
);
