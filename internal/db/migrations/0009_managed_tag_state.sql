-- Audio availability and presentation-tag outcome are independent.
ALTER TABLE managed_tracks ADD COLUMN tag_state TEXT NOT NULL DEFAULT 'pending' CHECK (tag_state IN ('pending', 'tagged', 'unsupported', 'failed'));
ALTER TABLE managed_tracks ADD COLUMN tag_error TEXT NOT NULL DEFAULT '' CHECK (length(tag_error) <= 128);
