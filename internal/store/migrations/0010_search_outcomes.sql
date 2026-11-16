-- Schema version 10: the manager's replacement search as its own outcome
-- (SPEC §7 step 6, §9.1), separate from the grab: a search command for the
-- same content queued after the recovery's manager request, seen accepted
-- (queued), running, completed, or failed. A completed search says nothing
-- about a grab. A search never seen is "not observed", never evidence that
-- it did not run: managers drop finished commands from their list after a
-- few minutes. Outcomes recorded before this version were not tracked.
ALTER TABLE recovery_outcomes ADD COLUMN search_state TEXT NOT NULL DEFAULT 'not_tracked';
ALTER TABLE recovery_outcomes ADD COLUMN search_since INTEGER;
ALTER TABLE recovery_outcomes ADD COLUMN search_command_id INTEGER;
ALTER TABLE recovery_outcomes ADD COLUMN search_name TEXT NOT NULL DEFAULT '';
ALTER TABLE recovery_outcomes ADD COLUMN search_queued_at INTEGER;
ALTER TABLE recovery_outcomes ADD COLUMN search_ended_at INTEGER;
ALTER TABLE recovery_outcomes ADD COLUMN search_checked_at INTEGER;
ALTER TABLE recovery_outcomes ADD COLUMN search_note TEXT NOT NULL DEFAULT '';
