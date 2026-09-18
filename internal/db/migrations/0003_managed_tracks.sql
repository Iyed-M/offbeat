-- Independent of current desired tracks: removal must retain file associations.
CREATE TABLE managed_tracks (
    track_uri TEXT PRIMARY KEY NOT NULL CHECK (length(track_uri) > 0),
    relative_path TEXT NOT NULL UNIQUE CHECK (length(relative_path) > 0)
);
