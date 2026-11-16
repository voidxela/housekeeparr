-- Schema version 7: transfers a workflow stopped and kept with their data
-- (SPEC §7.1). A row is recorded when a verified stop retains a transfer
-- and released only when the transfer leaves the client's complete
-- listing. Sizes are logical (wanted/verified bytes as the client reported
-- them when planned) and may be unknown; shared files and hardlinks make
-- their sum an estimate, not disk use. These records are not pruned by
-- observation retention.
CREATE TABLE retained_transfers (
    client_id       TEXT NOT NULL,
    native_id       TEXT NOT NULL,
    intent_id       INTEGER NOT NULL,
    name            TEXT NOT NULL DEFAULT '',
    manager_id      TEXT NOT NULL DEFAULT '',
    content         TEXT NOT NULL DEFAULT '',
    wanted_bytes    INTEGER,
    verified_bytes  INTEGER,
    retained_at     INTEGER NOT NULL,
    released_at     INTEGER,
    released_reason TEXT,
    PRIMARY KEY (client_id, native_id, intent_id)
);
CREATE INDEX retained_transfers_open ON retained_transfers(released_at);
