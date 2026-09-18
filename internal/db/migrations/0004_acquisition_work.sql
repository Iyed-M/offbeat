CREATE TABLE acquisition_work (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    track_uri  TEXT NOT NULL CHECK (length(track_uri) > 0),
    source_url TEXT NOT NULL CHECK (length(source_url) > 0),
    state      TEXT NOT NULL CHECK (state IN ('pending', 'running', 'failed', 'complete')),
    error      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- A track has at most one outstanding request, while completed and failed
-- records remain available for user-visible outcome and explicit retry.
CREATE UNIQUE INDEX acquisition_work_one_active_per_track
ON acquisition_work(track_uri)
WHERE state IN ('pending', 'running');
