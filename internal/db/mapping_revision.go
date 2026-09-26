package db

import "context"

// MappingRevision includes tombstones for deleted mappings.
func (d *DB) MappingRevision(ctx context.Context, uri string) (int64, error) {
	var revision int64
	err := d.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM manual_youtube_mapping_revisions WHERE track_uri=?), 0)`, uri).Scan(&revision)
	return revision, err
}
