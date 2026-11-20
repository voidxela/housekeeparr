-- Schema version 20: cooldowns record when they started, so a shorter
-- configured success cooldown applies to cooldowns already recorded
-- (effective end = since + current cooldown, never later than until).
-- Existing rows take the time of the action that set them ("action #N
-- (...)"), or, if that cannot be found, until minus the previous 24 h
-- default.
ALTER TABLE action_cooldowns ADD COLUMN since INTEGER;

UPDATE action_cooldowns SET since = (
    SELECT i.updated_at FROM action_intents i
    WHERE reason LIKE 'action #%'
      AND i.id = CAST(substr(reason, 9, instr(substr(reason, 9), ' ') - 1) AS INTEGER)
);

UPDATE action_cooldowns SET since = until - 86400000 WHERE since IS NULL;
