-- A tombstone survives removal so a remove/recreate cannot reuse an inspected state.
CREATE TABLE manual_youtube_mapping_revisions (track_uri TEXT PRIMARY KEY, revision INTEGER NOT NULL);
INSERT INTO manual_youtube_mapping_revisions SELECT track_uri, 1 FROM manual_youtube_mappings;
CREATE TRIGGER mapping_revision_insert AFTER INSERT ON manual_youtube_mappings BEGIN
  INSERT INTO manual_youtube_mapping_revisions VALUES (NEW.track_uri, 1)
  ON CONFLICT(track_uri) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER mapping_revision_update AFTER UPDATE ON manual_youtube_mappings BEGIN
  INSERT INTO manual_youtube_mapping_revisions VALUES (NEW.track_uri, 1)
  ON CONFLICT(track_uri) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER mapping_revision_delete AFTER DELETE ON manual_youtube_mappings BEGIN
  UPDATE manual_youtube_mapping_revisions SET revision=revision+1 WHERE track_uri=OLD.track_uri;
END;
