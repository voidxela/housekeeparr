-- Schema version 14: a durable notification outbox (SPEC §11.2). Events
-- are written in the same transaction as the change they report, then
-- delivered to configured targets with a persisted hourly rate limit and
-- bounded retries. Delivery state is per target; an event that cannot be
-- delivered within its retry budget or age is dropped with a reason, never
-- retried without bound. Sent and dropped rows are pruned with the audit
-- retention.
CREATE TABLE notification_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    target        TEXT NOT NULL,
    dedup_key     TEXT NOT NULL,
    kind          TEXT NOT NULL,
    finding_id    INTEGER,
    severity      TEXT NOT NULL,
    title         TEXT NOT NULL,
    body          TEXT NOT NULL,
    link          TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    next_at       INTEGER NOT NULL,
    attempts      INTEGER NOT NULL DEFAULT 0,
    sent_at       INTEGER,
    dropped_at    INTEGER,
    last_error    TEXT NOT NULL DEFAULT '',
    UNIQUE (target, dedup_key)
);
CREATE INDEX notification_events_pending ON notification_events(target, sent_at, dropped_at, next_at);
