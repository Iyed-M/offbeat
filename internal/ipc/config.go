package ipc

// ConfigResult is the result body returned for a "config" command. It
// carries the daemon's effective configuration, sanitized so that secret
// values never leave the daemon: today no field on the v1 config is a
// secret, but the result struct is the single boundary through which
// configuration reaches the CLI. Adding a secret to config.Config is a
// deliberate omission from ConfigResult, not an accidental leak.
//
// It includes every non-secret setting in the current configuration.
type ConfigResult struct {
	Paths          ConfigPaths          `json:"paths"`
	Logging        ConfigLogging        `json:"logging"`
	SpotifyAdapter ConfigSpotifyAdapter `json:"spotify_adapter"`
	Downloader     ConfigDownloader     `json:"downloader"`
	Acquisition    ConfigAcquisition    `json:"acquisition"`
	Sync           ConfigSync           `json:"sync"`
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

type ConfigLogging struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

type ConfigSpotifyAdapter struct {
	BindAddress string `json:"bind_address"`
	Port        int    `json:"port"`
}

type ConfigDownloader struct {
	YTDLPPath   string `json:"yt_dlp_path"`
	FFmpegPath  string `json:"ffmpeg_path"`
	FFprobePath string `json:"ffprobe_path"`
}

type ConfigAcquisition struct {
	Concurrency      int    `json:"concurrency"`
	TempRetryBackoff string `json:"temp_retry_backoff"`
	MaxTempRetries   int    `json:"max_temp_retries"`
}

type ConfigSync struct {
	HTTPSPort      int    `json:"https_port"`
	LANBindAddress string `json:"lan_bind_address"`
	PairingTimeout string `json:"pairing_timeout"`
}
