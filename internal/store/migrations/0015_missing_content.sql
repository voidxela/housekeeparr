-- Schema version 15: monitored missing content as its managers report it,
-- with Housekeeparr's proactive-search state (SPEC §9.1). A row is marked
-- absent only by a complete read of that manager's missing list. Search
-- history is kept with the row (searches, last search, next eligible
-- time), so restarts do not reset backoff; rows are kept while present and
-- pruned some time after they disappear.
CREATE TABLE missing_content (
    manager_id      TEXT NOT NULL,
    content_key     TEXT NOT NULL,          -- episode:<id> | movie:<id>
    series_id       INTEGER,
    title           TEXT NOT NULL,
    aired_at        INTEGER,
    available       INTEGER,                -- 1, 0, or NULL (unknown)
    first_seen      INTEGER NOT NULL,
    last_seen       INTEGER NOT NULL,
    present         INTEGER NOT NULL DEFAULT 1,
    gone_at         INTEGER,
    searches        INTEGER NOT NULL DEFAULT 0,
    last_search_at  INTEGER,
    last_intent_id  INTEGER,
    next_search_at  INTEGER,
    state           TEXT NOT NULL DEFAULT 'not_searched',
    note            TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (manager_id, content_key)
);
CREATE INDEX missing_content_due ON missing_content(present, next_search_at);
