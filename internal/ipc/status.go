package ipc

import "time"

// StatusResult is the result body returned for a "status" command. The
// fields are deliberately small: identity, lifecycle, and database state
// only. Sensitive or mutable configuration belongs to a future "config"
// command, not here.
type StatusResult struct {
	DaemonVersion string    `json:"daemon_version"`
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"started_at"`
	DBReady       bool      `json:"db_ready"`
	SchemaVersion int       `json:"schema_version"`
}
