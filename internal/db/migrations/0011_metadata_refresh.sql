-- Fingerprints describe committed presentation, independently of audio acquisition.
ALTER TABLE managed_tracks ADD COLUMN tag_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE managed_tracks ADD COLUMN artwork_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE managed_tracks ADD COLUMN file_sha256 TEXT NOT NULL DEFAULT '';
-- A complete staged replacement is recorded before its atomic publication.
ALTER TABLE managed_tracks ADD COLUMN refresh_intent TEXT NOT NULL DEFAULT '';
