-- Schema version 13: a physical transfer is a client instance plus its
-- native ID (SPEC §3.1), so a recovery's handled transfer records its
-- client. Outcomes recorded before carry it only in their intent's
-- subject; it is copied from there while that intent is still retained.
-- An outcome whose client cannot be recovered stays '' and is never used
-- to exempt a transfer from the correlated-stall hold.
--
-- Retained transfers also record what the workflow left behind: 'stopped'
-- (stopped with its data) or 'running' (left active, for example a private
-- torrent under private_recovery = "keep_running", or a stop that did not
-- take effect). Every row before this schema was recorded by a verified stop.
ALTER TABLE recovery_outcomes ADD COLUMN old_client_id TEXT NOT NULL DEFAULT '';
UPDATE recovery_outcomes SET old_client_id = COALESCE((
    SELECT json_extract(subject_json, '$.client_id') FROM action_intents WHERE action_intents.id = recovery_outcomes.intent_id), '');
CREATE INDEX recovery_outcomes_handled ON recovery_outcomes(old_client_id, old_download_id);
ALTER TABLE retained_transfers ADD COLUMN disposition TEXT NOT NULL DEFAULT 'stopped';
