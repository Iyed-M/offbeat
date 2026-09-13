package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/Iyed-M/offbeat/internal/config"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/logging"
)

type Daemon struct {
	Cfg    config.Config
	Logger *slog.Logger
	DB     *db.DB
}

type Options struct {
	ConfigPath string
	HomeDir    string
}

func NewDaemon(ctx context.Context, opts Options) (*Daemon, error) {
	loader := config.NewLoader(opts.HomeDir, opts.ConfigPath)
	cfg, err := loader.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	lvl, err := logging.ParseLevel(cfg.Logging.Level)
	if err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", cfg.Logging.Level, err)
	}
	fmtKind, err := logging.ParseFormat(cfg.Logging.Format)
	if err != nil {
		return nil, fmt.Errorf("invalid log format %q: %w", cfg.Logging.Format, err)
	}
	logger := logging.New(logging.Config{Level: lvl, Format: fmtKind})
	slog.SetDefault(logger)

	if err := ensureDirs(cfg); err != nil {
		return nil, err
	}

	d, err := db.OpenFile(ctx, cfg.Paths.Database)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	logger.Info("database ready", "path", cfg.Paths.Database)
	return &Daemon{Cfg: cfg, Logger: logger, DB: d}, nil
}

func ensureDirs(cfg config.Config) error {
	dirs := []string{
		cfg.Paths.ConfigDir,
		cfg.Paths.DataDir,
		cfg.Paths.StateDir,
		cfg.Paths.CacheDir,
		cfg.Paths.SocketDir,
		cfg.Paths.CertsDir,
		filepath.Dir(cfg.Paths.Database),
		filepath.Dir(cfg.Paths.LogFile),
		cfg.Paths.MusicRoot,
		filepath.Join(cfg.Paths.MusicRoot, "tracks"),
		filepath.Join(cfg.Paths.MusicRoot, "playlists"),
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}

func (d *Daemon) Close() error {
	if d == nil || d.DB == nil {
		return nil
	}
	return d.DB.Close()
}

type RunOptions struct {
	SignalCh <-chan os.Signal
}

func (d *Daemon) Run(ctx context.Context, opts RunOptions) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigCh := opts.SignalCh
	if sigCh == nil {
		sigCh = defaultSignalChan()
	}
	go func() {
		select {
		case s := <-sigCh:
			d.Logger.Info("signal received, shutting down", "signal", s.String())
			cancel()
		case <-ctx.Done():
		}
	}()

	d.Logger.Info("offbeatd started",
		"data_dir", d.Cfg.Paths.DataDir,
		"music_root", d.Cfg.Paths.MusicRoot,
	)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
	}()

	<-ctx.Done()
	wg.Wait()
	d.Logger.Info("offbeatd stopped")
	return nil
}

var ErrShutdown = errors.New("shutdown requested")

func IsShutdown(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrShutdown)
}
