CREATE TABLE manual_youtube_mappings (
    track_uri TEXT PRIMARY KEY NOT NULL CHECK (substr(track_uri, 1, 14) = 'spotify:track:' AND length(track_uri) BETWEEN 15 AND 36 AND substr(track_uri, 15) NOT GLOB '*[^A-Za-z0-9]*'),
    video_id TEXT NOT NULL CHECK (length(video_id) = 11 AND video_id NOT GLOB '*[^A-Za-z0-9_-]*'),
    provenance TEXT NOT NULL DEFAULT 'manual' CHECK (provenance = 'manual'),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
