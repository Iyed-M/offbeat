package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// RefreshIntent is persisted before replacing an existing file. If the daemon
// stops after rename but before the outcome commit, the digest identifies the
// exact published bytes on the next explicit refresh.
type RefreshIntent struct {
	SHA256             string `json:"sha256"`
	PreviousSHA        string `json:"previous_sha256"`
	Temporary          string `json:"temporary"`
	TagFingerprint     string `json:"tag_fingerprint"`
	ArtworkFingerprint string `json:"artwork_fingerprint"`
	TagState           string `json:"tag_state"`
	TagError           string `json:"tag_error"`
	ArtworkState       string `json:"artwork_state"`
	ArtworkError       string `json:"artwork_error"`
}

func (d *DB) BeginMetadataReplacement(ctx context.Context, uri, path, previousSHA string, intent RefreshIntent) error {
	data, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	result, err := d.ExecContext(ctx, `UPDATE managed_tracks SET refresh_intent=? WHERE track_uri=? AND relative_path=? AND file_sha256=?`, string(data), uri, path, previousSHA)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("managed file mapping changed during refresh")
	}
	return nil
}

func (d *DB) FinishMetadataReplacement(ctx context.Context, uri, path string, intent RefreshIntent) error {
	data, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	result, err := d.ExecContext(ctx, `UPDATE managed_tracks SET tag_state=?, tag_error=?, artwork_state=?, artwork_error=?, tag_fingerprint=?, artwork_fingerprint=?, file_sha256=?, refresh_intent='' WHERE track_uri=? AND relative_path=? AND refresh_intent=?`, intent.TagState, intent.TagError, intent.ArtworkState, intent.ArtworkError, intent.TagFingerprint, intent.ArtworkFingerprint, intent.SHA256, uri, path, string(data))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (d *DB) RecordMetadataOutcome(ctx context.Context, uri, path, tagState, tagError, artState, artError, tagHash, artHash, fileSHA string) error {
	result, err := d.ExecContext(ctx, `UPDATE managed_tracks SET tag_state=?, tag_error=?, artwork_state=?, artwork_error=?, tag_fingerprint=?, artwork_fingerprint=?, file_sha256=?, refresh_intent='' WHERE track_uri=? AND relative_path=?`, tagState, tagError, artState, artError, tagHash, artHash, fileSHA, uri, path)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
