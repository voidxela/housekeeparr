-- Schema version 17: whether the manager reported indexer status at all.
-- Released Sonarr 4.0.20 has no indexerstatus endpoint; its sources' status
-- is unknown, never assumed healthy. Rows stored before were read with
-- status (the read failed otherwise).
ALTER TABLE source_status ADD COLUMN status_known INTEGER NOT NULL DEFAULT 1;
