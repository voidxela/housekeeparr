-- Schema version 25: library coverage (read only). A bounded sweep reads
-- each manager's groups (Sonarr series; Radarr's movie list as one group)
-- and, a few per cycle, each group's items. coverage_groups holds every
-- group of the latest complete group list with its latest counts; a group
-- not read yet has read_at NULL. Only a complete group list removes a
-- group, so a partial, failed or rotated sweep never drops unseen items from
-- the denominator. coverage_gaps holds the wanted items without a file from
-- each group's latest read. coverage_daily keeps one row per manager and
-- UTC day, written when a sweep completes.
CREATE TABLE coverage_groups (
    manager_id TEXT NOT NULL,
    group_id   INTEGER NOT NULL,
    title      TEXT NOT NULL,
    monitored  INTEGER NOT NULL DEFAULT 1,
    wanted     INTEGER NOT NULL DEFAULT 0,
    have       INTEGER NOT NULL DEFAULT 0,
    read_at    INTEGER,
    PRIMARY KEY (manager_id, group_id)
);
CREATE TABLE coverage_gaps (
    manager_id  TEXT NOT NULL,
    content_key TEXT NOT NULL,
    group_id    INTEGER NOT NULL,
    label       TEXT NOT NULL,
    aired_at    INTEGER,
    first_seen  INTEGER NOT NULL,
    last_seen   INTEGER NOT NULL,
    PRIMARY KEY (manager_id, content_key)
);
CREATE INDEX coverage_gaps_group ON coverage_gaps(manager_id, group_id);
CREATE TABLE coverage_sweeps (
    manager_id       TEXT PRIMARY KEY,
    started_at       INTEGER NOT NULL,
    completed_at     INTEGER,
    last_complete_at INTEGER
);
CREATE TABLE coverage_daily (
    manager_id TEXT NOT NULL,
    day        TEXT NOT NULL,
    wanted     INTEGER NOT NULL,
    have       INTEGER NOT NULL,
    groups     INTEGER NOT NULL,
    at         INTEGER NOT NULL,
    PRIMARY KEY (manager_id, day)
);
