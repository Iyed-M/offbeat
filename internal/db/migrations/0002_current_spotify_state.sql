CREATE TABLE spotify_tracks (
    uri         TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    artists_json TEXT NOT NULL CHECK (json_valid(artists_json) AND json_type(artists_json) = 'array'),
    album_uri   TEXT NOT NULL,
    album_name  TEXT NOT NULL,
    duration_ms INTEGER NOT NULL CHECK (duration_ms > 0)
);

CREATE TABLE playlists (
    uri               TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    rootlist_position INTEGER NOT NULL UNIQUE CHECK (rootlist_position >= 0)
);

CREATE TABLE playlist_entries (
    playlist_uri TEXT NOT NULL REFERENCES playlists(uri) ON DELETE CASCADE,
    position     INTEGER NOT NULL CHECK (position >= 0),
    kind         TEXT NOT NULL CHECK (kind IN ('supported', 'unsupported')),
    track_uri    TEXT REFERENCES spotify_tracks(uri),
    source_uri   TEXT,
    PRIMARY KEY (playlist_uri, position),
    CHECK (source_uri IS NULL OR source_uri <> ''),
    CHECK (
        (kind = 'supported' AND track_uri IS NOT NULL AND source_uri IS NULL) OR
        (kind = 'unsupported' AND track_uri IS NULL)
    )
);

CREATE TABLE liked_entries (
    position   INTEGER PRIMARY KEY CHECK (position >= 0),
    kind       TEXT NOT NULL CHECK (kind IN ('supported', 'unsupported')),
    track_uri  TEXT REFERENCES spotify_tracks(uri),
    source_uri TEXT,
    CHECK (source_uri IS NULL OR source_uri <> ''),
    CHECK (
        (kind = 'supported' AND track_uri IS NOT NULL AND source_uri IS NULL) OR
        (kind = 'unsupported' AND track_uri IS NULL)
    )
);

CREATE TABLE state_metadata (
    singleton_id      INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    revision          INTEGER NOT NULL CHECK (revision >= 0),
    last_committed_at TEXT
);

INSERT INTO state_metadata(singleton_id, revision, last_committed_at)
VALUES (1, 0, NULL);
