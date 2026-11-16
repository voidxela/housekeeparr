-- Schema version 6: a per-step baseline of external evidence (for example
-- the manager's matching blocklist entry and history record IDs), read
-- immediately before the request and committed with the 'dispatching'
-- record. Verification then requires evidence that is new relative to the
-- baseline, so an older matching entry is never mistaken for this
-- action's effect. Steps journaled before this version have no baseline
-- and verify as unknown (held for review) when that evidence is needed.
ALTER TABLE action_steps ADD COLUMN baseline_json TEXT;
