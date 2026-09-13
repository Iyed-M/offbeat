package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

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

	lock, err := AcquireLock(cfg.Paths.StateDir)
	if err != nil {
		return nil, err
	}
	d := &Daemon{
		Cfg:       cfg,
		lock:      lock,
		socketDir: cfg.Paths.SocketDir,
	}

	if err := ensureDirs(cfg); err != nil {
		return nil, d.closeAfterError(err)
	}

	lvl, err := logging.ParseLevel(cfg.Logging.Level)
	if err != nil {
		return nil, d.closeAfterError(fmt.Errorf("invalid log level %q: %w", cfg.Logging.Level, err))
	}
	fmtKind, err := logging.ParseFormat(cfg.Logging.Format)
	if err != nil {
		return nil, d.closeAfterError(fmt.Errorf("invalid log format %q: %w", cfg.Logging.Format, err))
	}
	logger, err := logging.New(logging.Config{
		Level:  lvl,
		Format: fmtKind,
		File:   cfg.Paths.LogFile,
	})
	if err != nil {
		return nil, d.closeAfterError(fmt.Errorf("init logger: %w", err))
	}
	d.Logger = logger
	slog.SetDefault(logger)
	logger.Info("acquired daemon-owner lock", "lock", lock.Path())

	if err := EnforceSocketDirPermissions(cfg.Paths.SocketDir); err != nil {
		return nil, d.closeAfterError(err)
	}

	database, err := db.OpenFile(ctx, cfg.Paths.Database)
	if err != nil {
		return nil, d.closeAfterError(fmt.Errorf("open database: %w", err))
	}
	d.DB = database
	if _, err := d.DB.Migrate(ctx, nil, ""); err != nil {
		return nil, d.closeAfterError(fmt.Errorf("migrate database: %w", err))
	}
	logger.Info("database ready", "path", cfg.Paths.Database)

	return d, nil
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

// Close releases daemon ownership. It is safe to call after Run has already
// stopped the listener and torn down the database and lock; each step is
// independently nil-safe and idempotent.
func (d *Daemon) Close() error {
	if d == nil {
		return nil
	}
	return d.releaseResources()
}

func (d *Daemon) closeAfterError(cause error) error {
	return errors.Join(cause, d.releaseResources())
}

func (d *Daemon) releaseResources() error {
	var errs []error
	if d.DB != nil {
		if err := d.DB.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close database: %w", err))
		}
		d.DB = nil
	}
	if d.lock != nil {
		if err := d.lock.Release(); err != nil {
			errs = append(errs, fmt.Errorf("release daemon lock: %w", err))
		}
		d.lock = nil
	}
	return errors.Join(errs...)
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
		return d.shutdown(fmt.Errorf("resolve stale socket: %w", err))
	}
	if stale {
		d.Logger.Info("recovered stale control socket", "path", SocketPath(d.socketDir))
	}

	listener, err := BindControlSocket(d.socketDir)
	if err != nil {
		return d.shutdown(err)
	}

	d.Logger.Info("offbeatd ready", "socket", SocketPath(d.socketDir))

	var acceptErr error
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		if err := acceptLoop(ctx, listener); err != nil {
			acceptErr = err
		}
	}()

	select {
	case <-ctx.Done():
	case <-acceptDone:
		if acceptErr != nil {
			d.Logger.Error("control socket accept loop failed; shutting down", "err", acceptErr)
			cancel()
		}
	}

	d.Logger.Info("offbeatd stopping", "phase", "stop_accept")
	if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		d.Logger.Warn("listener close", "err", err)
	}
	<-acceptDone
	d.Logger.Info("offbeatd stopping", "phase", "drained")

	d.Logger.Info("offbeatd stopping", "phase", "remove_socket")
	if err := os.Remove(SocketPath(d.socketDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		d.Logger.Warn("remove socket", "err", err)
	}

	d.Logger.Info("offbeatd stopping", "phase", "close_db")
	d.Logger.Info("offbeatd stopping", "phase", "release_lock")
	if err := d.releaseResources(); err != nil {
		d.Logger.Warn("release daemon resources", "err", err)
	}

	d.Logger.Info("offbeatd stopped")

	if acceptErr != nil {
		return fmt.Errorf("control socket accept loop: %w", acceptErr)
	}
	return nil
}

// shutdown releases daemon ownership when Run cannot serve. It exists for the
// early-failure paths where the listener was never bound: tear down DB and lock
// in the agreed order so a future start can take over.
func (d *Daemon) shutdown(cause error) error {
	if err := d.releaseResources(); err != nil {
		d.Logger.Warn("release daemon resources", "err", err)
		return errors.Join(cause, err)
	}
	return cause
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
		if err := conn.Close(); err != nil {
			return fmt.Errorf("close accepted conn: %w", err)
		}
	}
}

var ErrShutdown = errors.New("shutdown requested")

func IsShutdown(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrShutdown)
}
