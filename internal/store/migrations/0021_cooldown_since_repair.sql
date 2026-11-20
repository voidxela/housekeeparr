-- Schema version 21: repair cooldown starts that migration 0020 could not
-- recover. 0020 parsed "action #N (...)"; a reason that is exactly
-- "action #N" (no trailing workflow) fell back to until minus 24 h, which
-- is wrong for any other configured cooldown. Where that action's intent
-- still exists, its completion time is the start. Rows whose action was
-- pruned keep the fallback: their start cannot be established.
UPDATE action_cooldowns SET since = (
    SELECT i.updated_at FROM action_intents i
    WHERE 'action #' || i.id = action_cooldowns.reason AND i.state = 'succeeded'
)
WHERE EXISTS (
    SELECT 1 FROM action_intents i
    WHERE 'action #' || i.id = action_cooldowns.reason AND i.state = 'succeeded'
);
