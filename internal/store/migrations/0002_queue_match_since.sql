-- Schema version 2: record when a queue row's correlation status was first
-- observed, so "not found in client" requires persistence across cycles
-- instead of racing between independent manager and client reads.
ALTER TABLE queue_items ADD COLUMN match_since INTEGER;
