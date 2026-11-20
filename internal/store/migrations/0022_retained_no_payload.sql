-- Schema version 22: retained transfers record positively that they hold
-- no payload (stopped before their metadata arrived), instead of that
-- being inferred from an unknown wanted size and zero verified bytes.
-- Rows recorded earlier keep NULL (not established); the usage summary
-- counts such a row as no-payload only while the client still lists the
-- transfer as acquiring metadata.
ALTER TABLE retained_transfers ADD COLUMN no_payload INTEGER;
