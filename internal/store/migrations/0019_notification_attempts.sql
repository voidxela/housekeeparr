-- Reserve every webhook request against its rate limit before dispatch.
CREATE TABLE notification_attempts (
    target TEXT NOT NULL,
    attempted_at INTEGER NOT NULL
);
CREATE INDEX notification_attempts_target_time ON notification_attempts(target, attempted_at);
CREATE INDEX notification_attempts_time ON notification_attempts(attempted_at);

-- Preserve rate usage from verifiable deliveries made with schema 18.
INSERT INTO notification_attempts(target, attempted_at)
    SELECT target, sent_at FROM notification_events WHERE sent_at IS NOT NULL;
