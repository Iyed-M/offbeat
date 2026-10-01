package ipc

// SyncSetupResult is private provisioning output, returned only by explicit
// setup/reset requests over the owner-only Control socket.
type SyncSetupResult struct {
	Address           string `json:"address"`
	CertificateSHA256 string `json:"certificate_sha256"`
	Credential        string `json:"credential"`
}

type SyncStatusResult struct {
	Enabled           bool   `json:"enabled"`
	Address           string `json:"address,omitempty"`
	CertificateSHA256 string `json:"certificate_sha256,omitempty"`
}
