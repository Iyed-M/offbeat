ALTER TABLE acquisition_work RENAME TO acquisition_work_m6;

CREATE TABLE acquisition_work (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    track_uri   TEXT NOT NULL CHECK (length(track_uri) > 0),
    source_kind TEXT NOT NULL CHECK (source_kind IN ('direct', 'youtube')),
    source_url  TEXT CHECK (source_url IS NULL OR length(source_url) > 0),
    state       TEXT NOT NULL CHECK (state IN ('pending', 'running', 'unresolved', 'failed', 'complete')),
    error       TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    CHECK (source_kind = 'youtube' OR source_url IS NOT NULL)
);

INSERT INTO acquisition_work(id, track_uri, source_kind, source_url, state, error, created_at, updated_at)
SELECT id, track_uri, 'direct', source_url, state, error, created_at, updated_at
FROM acquisition_work_m6;

DROP TABLE acquisition_work_m6;

CREATE UNIQUE INDEX acquisition_work_one_active_per_track
ON acquisition_work(track_uri)
WHERE state IN ('pending', 'running');

CREATE INDEX acquisition_work_track_source
ON acquisition_work(track_uri, source_kind);
