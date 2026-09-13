package ipc

// ConfigResult is the result body returned for a "config" command. It
// carries the daemon's effective configuration, sanitized so that secret
// values never leave the daemon: today no field on the v1 config is a
// secret, but the result struct is the single boundary through which
// configuration reaches the CLI. Adding a secret to config.Config is a
// deliberate omission from ConfigResult, not an accidental leak.
//
// Mirrors the surface that the CLI previously rendered from a local file
// (paths plus a few runtime knobs) so that the user-visible output of
// `offbeat config` does not regress when the command moves off the file
// and onto the control protocol.
type ConfigResult struct {
	Paths               ConfigPaths `json:"paths"`
	AcquisitionConcurrency int       `json:"acquisition_concurrency"`
	DownloaderYTDLPPath    string    `json:"downloader_yt_dlp_path"`
	DownloaderFFmpegPath   string    `json:"downloader_ffmpeg_path"`
	DownloaderFFprobePath  string    `json:"downloader_ffprobe_path"`
}

// ConfigPaths is the path subset of ConfigResult. Mirrors config.Paths
// field-for-field so the CLI can render it without mapping individual
// fields.
type ConfigPaths struct {
	ConfigDir string `json:"config_dir"`
	DataDir   string `json:"data_dir"`
	StateDir  string `json:"state_dir"`
	CacheDir  string `json:"cache_dir"`
	MusicRoot string `json:"music_root"`
	Database  string `json:"database"`
	SocketDir string `json:"socket_dir"`
	CertsDir  string `json:"certs_dir"`
	LogFile   string `json:"log_file"`
}
