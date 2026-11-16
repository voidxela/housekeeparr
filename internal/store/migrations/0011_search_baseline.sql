-- Schema version 11: the highest manager command ID listed just before the
-- recovery's manager request (journaled with that step's baseline). The
-- replacement search is attributed only to a newer command, so a search
-- queued before the request (by a user, or for the original grab) is never
-- mistaken for it. NULL when the list could not be read then; attribution
-- then falls back to the manager-native trigger and queued time.
ALTER TABLE recovery_outcomes ADD COLUMN search_after_id INTEGER;
