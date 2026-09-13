package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const AppDirName = "offbeat"

type Paths struct {
	ConfigDir string `toml:"config_dir"`
	DataDir   string `toml:"data_dir"`
	StateDir  string `toml:"state_dir"`
	CacheDir  string `toml:"cache_dir"`
	MusicRoot string `toml:"music_root"`
	Database  string `toml:"database"`
	SocketDir string `toml:"socket_dir"`
	CertsDir  string `toml:"certs_dir"`
	LogFile   string `toml:"log_file"`
}

type Logging struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
}

type SpotifyAdapter struct {
	BindAddress string `toml:"bind_address"`
	Port        int    `toml:"port"`
}

type Downloader struct {
	YTDLPPath   string `toml:"yt_dlp_path"`
	FFmpegPath  string `toml:"ffmpeg_path"`
	FFprobePath string `toml:"ffprobe_path"`
}

type Acquisition struct {
	Concurrency      int      `toml:"concurrency"`
	TempRetryBackoff Duration `toml:"temp_retry_backoff"`
	MaxTempRetries   int      `toml:"max_temp_retries"`
}

type Sync struct {
	HTTPSPort      int      `toml:"https_port"`
	LANBindAddress string   `toml:"lan_bind_address"`
	PairingTimeout Duration `toml:"pairing_timeout"`
}

type Config struct {
	Paths          Paths          `toml:"paths"`
	Logging        Logging        `toml:"logging"`
	SpotifyAdapter SpotifyAdapter `toml:"spotify_adapter"`
	Downloader     Downloader     `toml:"downloader"`
	Acquisition    Acquisition    `toml:"acquisition"`
	Sync           Sync           `toml:"sync"`
}

func Defaults(home string) Config {
	cfg := Config{}
	cfg.Paths.ConfigDir = filepath.Join(home, ".config", AppDirName)
	cfg.Paths.DataDir = filepath.Join(home, ".local", "share", AppDirName)
	cfg.Paths.StateDir = filepath.Join(home, ".local", "state", AppDirName)
	cfg.Paths.CacheDir = filepath.Join(home, ".cache", AppDirName)
	cfg.Paths.MusicRoot = filepath.Join(home, "Music", "Localify")
	cfg.Paths.Database = filepath.Join(cfg.Paths.DataDir, "offbeat.db")
	cfg.Paths.SocketDir = filepath.Join(cfg.Paths.StateDir, "ipc")
	cfg.Paths.CertsDir = filepath.Join(cfg.Paths.StateDir, "certs")
	cfg.Paths.LogFile = filepath.Join(cfg.Paths.StateDir, "log", "offbeatd.log")

	cfg.Logging.Level = "info"
	cfg.Logging.Format = "text"

	cfg.SpotifyAdapter.BindAddress = "127.0.0.1"
	cfg.SpotifyAdapter.Port = 0

	cfg.Downloader.YTDLPPath = "yt-dlp"
	cfg.Downloader.FFmpegPath = "ffmpeg"
	cfg.Downloader.FFprobePath = "ffprobe"

	cfg.Acquisition.Concurrency = 2
	cfg.Acquisition.TempRetryBackoff = Duration(30 * time.Second)
	cfg.Acquisition.MaxTempRetries = 5

	cfg.Sync.HTTPSPort = 0
	cfg.Sync.LANBindAddress = "0.0.0.0"
	cfg.Sync.PairingTimeout = Duration(5 * time.Minute)

	return cfg
}

func UserHomeDir() (string, error) {
	if v := os.Getenv("OFFBEAT_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	if home == "" {
		if runtime.GOOS == "windows" {
			return "", errors.New("cannot resolve user home directory")
		}
		return "/tmp", nil
	}
	return home, nil
}

func DefaultConfigPath() (string, error) {
	home, err := UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", AppDirName, "config.toml"), nil
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	if err != nil {
		return false
	}
	return !st.IsDir()
}

type Loader struct {
	configPath string
	home       string
}

func NewLoader(home, configPath string) *Loader {
	return &Loader{home: home, configPath: configPath}
}

func (l *Loader) Load() (Config, error) {
	home := l.home
	if home == "" {
		h, err := UserHomeDir()
		if err != nil {
			return Config{}, err
		}
		home = h
	}
	cfg := Defaults(home)
	if l.configPath == "" {
		cp, err := DefaultConfigPath()
		if err != nil {
			return cfg, err
		}
		l.configPath = cp
	}
	if fileExists(l.configPath) {
		if err := applyTOMLFromPath(l.configPath, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", l.configPath, err)
		}
		cfg = applyDerived(cfg, home)
	}
	return cfg, nil
}

func applyDerived(cfg Config, home string) Config {
	if !filepath.IsAbs(cfg.Paths.Database) {
		cfg.Paths.Database = filepath.Join(cfg.Paths.DataDir, "offbeat.db")
	}
	if !filepath.IsAbs(cfg.Paths.MusicRoot) {
		cfg.Paths.MusicRoot = filepath.Join(home, "Music", "Localify")
	}
	return cfg
}
