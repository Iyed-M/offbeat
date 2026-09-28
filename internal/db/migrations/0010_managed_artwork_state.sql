-- Artwork failure is retryable independently of audio availability and text tags.
ALTER TABLE managed_tracks ADD COLUMN artwork_state TEXT NOT NULL DEFAULT 'pending' CHECK (artwork_state IN ('pending', 'embedded', 'unavailable', 'unsupported', 'failed'));
ALTER TABLE managed_tracks ADD COLUMN artwork_error TEXT NOT NULL DEFAULT '' CHECK (length(artwork_error) <= 128);
