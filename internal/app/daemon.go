package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/config"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/ipc"
	"github.com/Iyed-M/offbeat/internal/logging"
	"github.com/Iyed-M/offbeat/internal/managed"
	"github.com/coder/websocket"
)

type Daemon struct {
	Cfg    config.Config
	Logger *slog.Logger
	DB     *db.DB

	startedAt time.Time
	version   string

	lock      *Lock
	socketDir string

	adapterCredential  string
	adapterListener    net.Listener
	adapterServer      *http.Server
	adapterServeDone   chan struct{}
	adapterMu          sync.Mutex
	adapterSession     *adapterSession
	adapterReserved    bool
	adapterStopping    bool
	pendingSnapshot    *pendingSnapshot
	adapterConnections map[*websocket.Conn]struct{}
	controlHandlers    sync.WaitGroup
	snapshotTimeout    time.Duration
	livenessWindow     time.Duration
	livenessInterval   time.Duration
	managedFiles       *managed.Files
	managedMu          sync.Mutex
	retriever          acquisition.Retriever
	resolver           acquisition.Resolver
	acquisitionCancel  context.CancelFunc
	acquisitionWorkers sync.WaitGroup
	syntheticFixtures  bool
}

type Options struct {
	ConfigPath string
	HomeDir    string
	Version    string
	// AdapterCredential is development/test-provisioned secret material. M12
	// will replace this source with setup-managed secret storage.
	AdapterCredential string
	// EnableSyntheticFixtures enables the in-process development/test seam only.
	// It is deliberately not exposed by offbeatd flags or the Control protocol.
	EnableSyntheticFixtures bool
	// Retriever overrides media retrieval for controlled acceptance tests.
	Retriever acquisition.Retriever
	// Resolver overrides YouTube selection for controlled acceptance tests.
	Resolver acquisition.Resolver
}

var ErrAdapterCredentialUnavailable = errors.New("adapter credential is not provisioned")

const m3SnapshotTimeout = 5 * time.Minute

func NewDaemon(ctx context.Context, opts Options) (*Daemon, error) {
	loader := config.NewLoader(opts.HomeDir, opts.ConfigPath)
	cfg, err := loader.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if opts.AdapterCredential == "" {
		return nil, ErrAdapterCredentialUnavailable
	}

	lock, err := AcquireLock(cfg.Paths.StateDir)
	if err != nil {
		return nil, err
	}
	d := &Daemon{
		Cfg:               cfg,
		lock:              lock,
		socketDir:         cfg.Paths.SocketDir,
		adapterCredential: opts.AdapterCredential,
		startedAt:         time.Now().UTC(),
		version:           opts.Version,
		snapshotTimeout:   m3SnapshotTimeout,
		livenessWindow:    30 * time.Second,
		livenessInterval:  10 * time.Second,
		syntheticFixtures: opts.EnableSyntheticFixtures,
		retriever:         opts.Retriever,
		resolver:          opts.Resolver,
	}

	if err := ensureDirs(cfg); err != nil {
		return nil, d.closeAfterError(err)
	}
	d.managedFiles, err = managed.Open(cfg.Paths.MusicRoot)
	if err != nil {
		return nil, d.closeAfterError(fmt.Errorf("open managed root: %w", err))
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
	if d.retriever == nil {
		d.retriever = acquisition.NewRetriever(cfg.Downloader)
	}
	if d.resolver == nil {
		d.resolver = acquisition.NewYouTubeResolver(cfg.Downloader)
	}

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
	d.stopAcquisitionWork()
	d.stopAdapterWork()
	if d.adapterServer != nil {
		if err := d.adapterServer.Shutdown(context.Background()); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, fmt.Errorf("close adapter endpoint: %w", err))
		}
		d.adapterServer = nil
	}
	if d.adapterListener != nil {
		if err := d.adapterListener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("close adapter listener: %w", err))
		}
		d.adapterListener = nil
	}
	if d.adapterServeDone != nil {
		<-d.adapterServeDone
		d.adapterServeDone = nil
	}
	if d.DB != nil {
		if err := d.DB.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close database: %w", err))
		}
		d.DB = nil
	}
	if d.managedFiles != nil {
		if err := d.managedFiles.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close managed root: %w", err))
		}
		d.managedFiles = nil
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

	adapterListener, err := BindAdapterListener(d.Cfg.SpotifyAdapter)
	if err != nil {
		return d.shutdown(fmt.Errorf("bind Spotify adapter listener: %w", err))
	}
	d.adapterListener = adapterListener
	adapterServer := &http.Server{
		Handler:     d.adapterHandler(ctx),
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	adapterServeDone := make(chan struct{})
	d.adapterServer = adapterServer
	d.adapterServeDone = adapterServeDone
	go func() {
		defer close(adapterServeDone)
		if err := adapterServer.Serve(adapterListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.Logger.Error("adapter endpoint serve failed", "err", err)
		}
	}()

	stale, err := ResolveStaleSocket(d.socketDir)
	if err != nil {
		return d.shutdown(fmt.Errorf("resolve stale socket: %w", err))
	}
	if stale {
		d.Logger.Info("recovered stale control socket", "path", SocketPath(d.socketDir))
	}

	d.managedMu.Lock()
	reconcileErr := d.materializePlaylistsLocked(ctx)
	d.managedMu.Unlock()
	if reconcileErr != nil {
		if ctx.Err() != nil {
			return d.shutdown(nil)
		}
		d.Logger.Error("reconcile desktop playlists before readiness")
		return d.shutdown(errors.New("reconcile desktop playlists before readiness"))
	}

	listener, err := BindControlSocket(d.socketDir)
	if err != nil {
		return d.shutdown(err)
	}

	if err := d.startAcquisitionWork(ctx); err != nil {
		_ = listener.Close()
		_ = os.Remove(SocketPath(d.socketDir))
		if ctx.Err() != nil {
			return d.shutdown(nil)
		}
		return d.shutdown(fmt.Errorf("start acquisition: %w", err))
	}
	d.Logger.Info("offbeatd ready", "socket", SocketPath(d.socketDir))

	var acceptErr error
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		if err := serveLoop(ctx, listener, d.handleControlRequest, d.Logger, &d.controlHandlers); err != nil {
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
	d.stopAdapterWork()
	if err := d.adapterServer.Shutdown(context.Background()); err != nil && !errors.Is(err, http.ErrServerClosed) {
		d.Logger.Warn("close adapter endpoint", "err", err)
	}
	<-d.adapterServeDone
	d.adapterServer = nil
	d.adapterListener = nil
	d.adapterServeDone = nil
	if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		d.Logger.Warn("listener close", "err", err)
	}
	<-acceptDone
	d.controlHandlers.Wait()
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

func (d *Daemon) adapterHandler(ctx context.Context) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(AdapterRoute, func(w http.ResponseWriter, r *http.Request) {
		d.serveAdapter(ctx, w, r)
	})
	return mux
}

// BindAdapterListener binds the daemon-owned adapter transport. Protocol and
// WebSocket handling are intentionally added by the following M2 ticket.
func BindAdapterListener(adapter config.SpotifyAdapter) (net.Listener, error) {
	if err := config.ValidateSpotifyAdapter(adapter); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(adapter.BindAddress, fmt.Sprintf("%d", adapter.Port)))
	if err != nil {
		return nil, err
	}
	return listener, nil
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

// serveLoop accepts one connection at a time and dispatches each request
// through the versioned JSON Lines control protocol. Each connection is
// served by its own goroutine; the connection is closed after exactly one
// request–response exchange (per ADR 0002).
func serveLoop(ctx context.Context, listener net.Listener, handler ipc.Handler, logger *slog.Logger, handlers *sync.WaitGroup) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		if handlers != nil {
			handlers.Add(1)
		}
		go func(c net.Conn) {
			if handlers != nil {
				defer handlers.Done()
			}
			defer func() {
				if r := recover(); r != nil && logger != nil {
					logger.Error("panic serving control connection", "err", r)
				}
			}()
			if err := ipc.Serve(ctx, c, handler, logger); err != nil && logger != nil {
				logger.Debug("control connection ended", "err", err)
			}
		}(conn)
	}
}

// handleControlRequest dispatches a single decoded control request to the
// matching command handler. Unknown commands return a structured
// invalid_request error so the CLI can render them uniformly.
func (d *Daemon) handleControlRequest(ctx context.Context, req ipc.Request) (any, error) {
	switch req.Command {
	case "status":
		return d.handleStatus(ctx)
	case "config":
		return d.handleConfig(ctx)
	case "spotify.sync":
		return d.handleSpotifySync(ctx)
	case "acquire", "acquire.missing", "acquire.status", "acquire.retry", "acquire.retry.unresolved":
		return d.handleAcquisition(ctx, req)
	case "missing":
		return d.handleMissing(ctx, req.Missing)
	default:
		return nil, ipc.NewError(ipc.CodeInvalidRequest,
			fmt.Sprintf("unknown command %q", req.Command))
	}
}

// handleStatus returns the M1.2 status payload: daemon identity, lifecycle
// and database readiness. It does not touch any mutable daemon state.
func (d *Daemon) handleStatus(ctx context.Context) (any, error) {
	if d.DB == nil {
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "database not ready")
	}
	schemaVersion, err := db.SchemaVersion(ctx, d.DB)
	if err != nil {
		return nil, fmt.Errorf("read schema version: %w", err)
	}
	return ipc.StatusResult{
		DaemonVersion:    d.version,
		PID:              os.Getpid(),
		StartedAt:        d.startedAt,
		DBReady:          true,
		SchemaVersion:    schemaVersion,
		AdapterConnected: d.adapterConnected(),
	}, nil
}

// handleConfig returns the M1.3 config payload: the daemon's effective
// configuration, sanitized through SanitizedConfig so secret values never
// leave the daemon. It reports the in-memory config the daemon loaded at
// startup; local file changes after startup are invisible until restart,
// which is the property the CLI relies on.
func (d *Daemon) handleConfig(_ context.Context) (any, error) {
	return SanitizedConfig(d.Cfg), nil
}

// SanitizedConfig projects config.Config onto ipc.ConfigResult. It is the
// single boundary through which configuration reaches the control
// protocol: any field not represented on the result is omitted by
// construction, which keeps secret additions to config.Config from
// leaking silently. The v1 config has no secret fields, so today this is
// a direct mapping.
func SanitizedConfig(cfg config.Config) ipc.ConfigResult {
	return ipc.ConfigResult{
		Paths: ipc.ConfigPaths{
			ConfigDir: cfg.Paths.ConfigDir,
			DataDir:   cfg.Paths.DataDir,
			StateDir:  cfg.Paths.StateDir,
			CacheDir:  cfg.Paths.CacheDir,
			MusicRoot: cfg.Paths.MusicRoot,
			Database:  cfg.Paths.Database,
			SocketDir: cfg.Paths.SocketDir,
			CertsDir:  cfg.Paths.CertsDir,
			LogFile:   cfg.Paths.LogFile,
		},
		Logging: ipc.ConfigLogging{
			Level:  cfg.Logging.Level,
			Format: cfg.Logging.Format,
		},
		SpotifyAdapter: ipc.ConfigSpotifyAdapter{
			BindAddress: cfg.SpotifyAdapter.BindAddress,
			Port:        cfg.SpotifyAdapter.Port,
		},
		Downloader: ipc.ConfigDownloader{
			YTDLPPath:   cfg.Downloader.YTDLPPath,
			FFmpegPath:  cfg.Downloader.FFmpegPath,
			FFprobePath: cfg.Downloader.FFprobePath,
		},
		Acquisition: ipc.ConfigAcquisition{
			Concurrency:      cfg.Acquisition.Concurrency,
			TempRetryBackoff: cfg.Acquisition.TempRetryBackoff.Std().String(),
			MaxTempRetries:   cfg.Acquisition.MaxTempRetries,
		},
		Sync: ipc.ConfigSync{
			HTTPSPort:      cfg.Sync.HTTPSPort,
			LANBindAddress: cfg.Sync.LANBindAddress,
			PairingTimeout: cfg.Sync.PairingTimeout.Std().String(),
		},
	}
}

var ErrShutdown = errors.New("shutdown requested")

func IsShutdown(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrShutdown)
}
