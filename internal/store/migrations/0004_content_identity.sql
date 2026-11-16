-- Schema version 4: content locks and cooldowns use the manager content
-- identity ("content:<manager>|episode:<id>" or "...|movie:<id>").
--
-- Earlier builds keyed them with the queue item key, which includes the
-- release's download ID, so a replacement release for the same episode or
-- movie did not share them. For every held lock and recorded cooldown
-- whose intent journaled a manager step, add the release-independent
-- subject derived from that step's target. Old subjects are kept: locks
-- are released with their intent and cooldowns expire, and nothing reads
-- the old form any more.

INSERT OR IGNORE INTO action_locks(subject, intent_id, acquired_at)
SELECT 'content:' || json_extract(s.target_json, '$.manager_id') || '|' ||
       CASE WHEN json_extract(s.target_json, '$.movie_id') > 0
            THEN 'movie:' || json_extract(s.target_json, '$.movie_id')
            ELSE 'episode:' || json_extract(s.target_json, '$.episode_id') END,
       l.intent_id, l.acquired_at
FROM action_locks l
JOIN action_steps s ON s.intent_id = l.intent_id AND s.op = 'manager.queue.fail'
WHERE l.subject LIKE 'content:%'
  AND json_extract(s.target_json, '$.manager_id') <> ''
  AND (json_extract(s.target_json, '$.movie_id') > 0 OR json_extract(s.target_json, '$.episode_id') > 0);

-- Cooldown reasons are "action #<id> (<workflow>)".
INSERT INTO action_cooldowns(subject, until, reason)
SELECT 'content:' || json_extract(s.target_json, '$.manager_id') || '|' ||
       CASE WHEN json_extract(s.target_json, '$.movie_id') > 0
            THEN 'movie:' || json_extract(s.target_json, '$.movie_id')
            ELSE 'episode:' || json_extract(s.target_json, '$.episode_id') END,
       c.until, c.reason
FROM action_cooldowns c
JOIN action_steps s ON s.op = 'manager.queue.fail'
     AND s.intent_id = CAST(substr(c.reason, instr(c.reason, '#') + 1) AS INTEGER)
WHERE c.subject LIKE 'content:%' AND instr(c.reason, '#') > 0
  AND json_extract(s.target_json, '$.manager_id') <> ''
  AND (json_extract(s.target_json, '$.movie_id') > 0 OR json_extract(s.target_json, '$.episode_id') > 0)
ON CONFLICT(subject) DO UPDATE SET until = max(until, excluded.until), reason = excluded.reason;
