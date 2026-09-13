package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
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

	lock      *Lock
	socketDir string
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

	if err := ensureDirs(cfg); err != nil {
		return nil, err
	}

	lvl, err := logging.ParseLevel(cfg.Logging.Level)
	if err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", cfg.Logging.Level, err)
	}
	fmtKind, err := logging.ParseFormat(cfg.Logging.Format)
	if err != nil {
		return nil, fmt.Errorf("invalid log format %q: %w", cfg.Logging.Format, err)
	}
	logger, err := logging.New(logging.Config{
		Level:  lvl,
		Format: fmtKind,
		File:   cfg.Paths.LogFile,
	})
	if err != nil {
		return nil, fmt.Errorf("init logger: %w", err)
	}
	slog.SetDefault(logger)

	lock, err := AcquireLock(cfg.Paths.StateDir)
	if err != nil {
		return nil, err
	}
	logger.Info("acquired daemon-owner lock", "lock", lock.Path())

	if err := EnforceSocketDirPermissions(cfg.Paths.SocketDir); err != nil {
		_ = lock.Release()
		return nil, err
	}

	d, err := db.OpenFile(ctx, cfg.Paths.Database)
	if err != nil {
		_ = lock.Release()
		return nil, fmt.Errorf("open database: %w", err)
	}
	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		_ = d.Close()
		_ = lock.Release()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	logger.Info("database ready", "path", cfg.Paths.Database)

	return &Daemon{
		Cfg:       cfg,
		Logger:    logger,
		DB:        d,
		lock:      lock,
		socketDir: cfg.Paths.SocketDir,
	}, nil
}

func ensureDirs(cfg config.Config) error {
	type dirSpec struct {
		path string
		mode os.FileMode
	}
	dirs := []dirSpec{
		{cfg.Paths.ConfigDir, 0o755},
		{cfg.Paths.DataDir, 0o755},
		{cfg.Paths.StateDir, 0o700},
		{cfg.Paths.CacheDir, 0o755},
		{cfg.Paths.SocketDir, 0o700},
		{cfg.Paths.CertsDir, 0o700},
		{filepath.Dir(cfg.Paths.Database), 0o755},
		{filepath.Dir(cfg.Paths.LogFile), 0o755},
		{cfg.Paths.MusicRoot, 0o755},
		{filepath.Join(cfg.Paths.MusicRoot, "tracks"), 0o755},
		{filepath.Join(cfg.Paths.MusicRoot, "playlists"), 0o755},
	}
	for _, d := range dirs {
		if d.path == "" {
			continue
		}
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return fmt.Errorf("mkdir %s: %w", d.path, err)
		}
	}
	return nil
}

// Close releases daemon ownership. It is safe to call after Run has already stopped
// the listener and closed the database; each step is independently nil-safe.
func (d *Daemon) Close() error {
	if d == nil {
		return nil
	}
	var firstErr error
	if d.DB != nil {
		if err := d.DB.Close(); err != nil {
			firstErr = err
		}
		d.DB = nil
	}
	if d.lock != nil {
		if err := d.lock.Release(); err != nil && firstErr == nil {
			firstErr = err
		}
		d.lock = nil
	}
	return firstErr
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
		"socket", SocketPath(d.socketDir),
	)

	stale, err := ResolveStaleSocket(d.socketDir)
	if err != nil {
		return fmt.Errorf("resolve stale socket: %w", err)
	}
	if stale {
		d.Logger.Info("recovered stale control socket", "path", SocketPath(d.socketDir))
	}

	listener, err := BindControlSocket(d.socketDir)
	if err != nil {
		return err
	}

	d.Logger.Info("offbeatd ready", "socket", SocketPath(d.socketDir))

	var acceptErr error
	acceptErrCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := acceptLoop(ctx, listener); err != nil {
			acceptErrCh <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-acceptErrCh:
		acceptErr = err
		d.Logger.Error("control socket accept loop failed; shutting down", "err", err)
		cancel()
	}

	d.Logger.Info("offbeatd stopping", "phase", "stop_accept")
	_ = listener.Close()
	wg.Wait()
	d.Logger.Info("offbeatd stopping", "phase", "drained")

	d.Logger.Info("offbeatd stopping", "phase", "remove_socket")
	if err := os.Remove(SocketPath(d.socketDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		d.Logger.Warn("remove socket", "err", err)
	}

	d.Logger.Info("offbeatd stopped")

	if acceptErr != nil {
		return fmt.Errorf("control socket accept loop: %w", acceptErr)
	}
	return nil
}

// acceptLoop is the placeholder M1.1 serve loop: accept a connection and close it
// immediately. The IPC protocol arrives in M1.2.
func acceptLoop(ctx context.Context, listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		_ = conn.Close()
	}
}

var ErrShutdown = errors.New("shutdown requested")

func IsShutdown(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrShutdown)
}
