package ipc

type MetadataDiagnostic struct {
	TrackURI string `json:"track_uri"`
	Reason   string `json:"reason"`
}

type MetadataRefreshResult struct {
	Considered      int                  `json:"considered"`
	Changed         int                  `json:"changed"`
	Partial         int                  `json:"partial"`
	Skipped         int                  `json:"skipped"`
	Failed          int                  `json:"failed"`
	Missing         int                  `json:"missing"`
	MissingOptional int                  `json:"missing_optional"`
	Diagnostics     []MetadataDiagnostic `json:"diagnostics"`
	Omitted         int                  `json:"omitted"`
}
