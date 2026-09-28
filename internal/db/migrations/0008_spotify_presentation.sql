ALTER TABLE spotify_tracks ADD COLUMN album_artist TEXT;
ALTER TABLE spotify_tracks ADD COLUMN track_number INTEGER CHECK (track_number IS NULL OR track_number BETWEEN 1 AND 9999);
ALTER TABLE spotify_tracks ADD COLUMN disc_number INTEGER CHECK (disc_number IS NULL OR disc_number BETWEEN 1 AND 9999);
ALTER TABLE spotify_tracks ADD COLUMN release_date TEXT;
ALTER TABLE spotify_tracks ADD COLUMN artwork_url TEXT;
