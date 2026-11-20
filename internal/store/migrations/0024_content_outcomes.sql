-- Schema version 24: per-content outcomes for a recovery that covered
-- several content items (a multi-episode release). Each episode's
-- replacement grab, progress and completion is followed separately; the
-- outcome's own columns summarise them. A JSON array; empty for an outcome
-- with one content item and for every outcome recorded before.
ALTER TABLE recovery_outcomes ADD COLUMN content_outcomes TEXT NOT NULL DEFAULT '';
