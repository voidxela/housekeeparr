-- Schema version 5: an attempt ledger replaces aligned budget counters.
--
-- One row per budget unit an intent reserved. Pacing scopes
-- ("pacing:global", "pacing:manager:<id>") count rows within a rolling
-- window. Attempt scopes ("attempts:<manager>|episode:<id>") count rows
-- since the scope's last explicit reset, with no automatic expiry
-- (SPEC §7: attempts reaching their budget require review or an explicit
-- policy reset). A reservation is released only when its request provably
-- was never sent or was refused before any effect; release keeps the row.
-- Rows outlive pruned intents, so attempt history survives replacements,
-- restarts, and retention.

CREATE TABLE action_attempts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    intent_id       INTEGER NOT NULL,
    scope           TEXT NOT NULL,
    units           INTEGER NOT NULL,
    at              INTEGER NOT NULL,
    released_at     INTEGER,
    released_reason TEXT
);
CREATE INDEX action_attempts_scope ON action_attempts(scope, at);
CREATE INDEX action_attempts_intent ON action_attempts(intent_id);

CREATE TABLE action_budget_resets (
    scope TEXT NOT NULL,
    at    INTEGER NOT NULL,
    actor TEXT NOT NULL,
    note  TEXT NOT NULL
);
CREATE INDEX action_budget_resets_scope ON action_budget_resets(scope, at);

-- Rebuild history from the journal: every intent whose manager step was
-- dispatched counts, conservatively, whatever its outcome.
INSERT INTO action_attempts(intent_id, scope, units, at)
SELECT s.intent_id, 'pacing:global', 1, COALESCE(s.dispatched_at, i.created_at)
FROM action_steps s JOIN action_intents i ON i.id = s.intent_id
WHERE s.op = 'manager.queue.fail' AND s.state NOT IN ('pending', 'skipped');

INSERT INTO action_attempts(intent_id, scope, units, at)
SELECT s.intent_id, 'pacing:manager:' || json_extract(s.target_json, '$.manager_id'), 1, COALESCE(s.dispatched_at, i.created_at)
FROM action_steps s JOIN action_intents i ON i.id = s.intent_id
WHERE s.op = 'manager.queue.fail' AND s.state NOT IN ('pending', 'skipped')
  AND json_extract(s.target_json, '$.manager_id') <> '';

INSERT INTO action_attempts(intent_id, scope, units, at)
SELECT s.intent_id, 'attempts:' || json_extract(s.target_json, '$.manager_id') || '|' ||
       CASE WHEN json_extract(s.target_json, '$.movie_id') > 0
            THEN 'movie:' || json_extract(s.target_json, '$.movie_id')
            ELSE 'episode:' || json_extract(s.target_json, '$.episode_id') END,
       1, COALESCE(s.dispatched_at, i.created_at)
FROM action_steps s JOIN action_intents i ON i.id = s.intent_id
WHERE s.op = 'manager.queue.fail' AND s.state NOT IN ('pending', 'skipped')
  AND json_extract(s.target_json, '$.manager_id') <> ''
  AND (json_extract(s.target_json, '$.movie_id') > 0 OR json_extract(s.target_json, '$.episode_id') > 0);

DROP TABLE action_budgets;
