package app

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

const (
	lockFileName   = "offbeatd.lock"
	socketFileName = "offbeatd.sock"
	AdapterRoute   = "/v1/adapter"
)

var ErrLockHeld = errors.New("another daemon owner holds the lock for this directory")

var ErrNonSocketAtSocketPath = errors.New("non-socket entry at socket path")

var ErrSocketDirPermissions = errors.New("socket directory permissions are not owner-only")

type Lock struct {
	fd   *os.File
	path string
}

func LockPath(stateDir string) string {
	return filepath.Join(stateDir, lockFileName)
}

func SocketPath(socketDir string) string {
	return filepath.Join(socketDir, socketFileName)
}

// AdapterEndpoint returns the fixed, versioned adapter endpoint for an
// already validated adapter listener configuration.
func AdapterEndpoint(bindAddress string, port int) string {
	return "ws://" + net.JoinHostPort(bindAddress, fmt.Sprintf("%d", port)) + AdapterRoute
}

func AcquireLock(stateDir string) (*Lock, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir state dir: %w", err)
	}
	path := LockPath(stateDir)
	fd, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	flockErr := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if flockErr == nil {
		return &Lock{fd: fd, path: path}, nil
	}
	closeErr := fd.Close()
	switch {
	case errors.Is(flockErr, syscall.EWOULDBLOCK), errors.Is(flockErr, syscall.EAGAIN):
		if closeErr != nil {
			return nil, fmt.Errorf("%w (close lock fd: %v)", ErrLockHeld, closeErr)
		}
		return nil, ErrLockHeld
	default:
		if closeErr != nil {
			return nil, fmt.Errorf("flock %s: %w (close lock fd: %v)", path, flockErr, closeErr)
		}
		return nil, fmt.Errorf("flock %s: %w", path, flockErr)
	}
}

func (l *Lock) Release() error {
	if l == nil || l.fd == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.fd.Fd()), syscall.LOCK_UN)
	closeErr := l.fd.Close()
	l.fd = nil
	switch {
	case unlockErr != nil && closeErr != nil:
		return fmt.Errorf("unlock lock file %s: %w; close lock file: %v", l.path, unlockErr, closeErr)
	case unlockErr != nil:
		return fmt.Errorf("unlock lock file %s: %w", l.path, unlockErr)
	case closeErr != nil:
		return fmt.Errorf("close lock file %s: %w", l.path, closeErr)
	}
	return nil
}

func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func EnforceSocketDirPermissions(socketDir string) error {
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		return fmt.Errorf("mkdir socket dir %s: %w", socketDir, err)
	}
	info, err := os.Stat(socketDir)
	if err != nil {
		return fmt.Errorf("stat socket dir %s: %w", socketDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrSocketDirPermissions, socketDir)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		return fmt.Errorf("%w: %s has mode %#o, want 0700",
			ErrSocketDirPermissions, socketDir, perm)
	}
	return nil
}

func ResolveStaleSocket(socketDir string) (bool, error) {
	path := SocketPath(socketDir)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat socket path %s: %w", path, err)
	}
	mode := info.Mode()
	switch {
	case mode&os.ModeSocket != 0:
		if err := os.Remove(path); err != nil {
			return false, fmt.Errorf("remove stale socket %s: %w", path, err)
		}
		return true, nil
	default:
		return false, fmt.Errorf("%w: %s (mode %s, type %s)",
			ErrNonSocketAtSocketPath, path, mode.Perm(), mode.Type())
	}
}

func BindControlSocket(socketDir string) (net.Listener, error) {
	path := SocketPath(socketDir)
	addr := &net.UnixAddr{Name: path, Net: "unix"}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		closeErr := listener.Close()
		removeErr := os.Remove(path)
		switch {
		case closeErr != nil && removeErr != nil:
			return nil, fmt.Errorf("chmod socket %s: %w (close listener: %v; remove path: %v)",
				path, err, closeErr, removeErr)
		case closeErr != nil:
			return nil, fmt.Errorf("chmod socket %s: %w (close listener: %v)", path, err, closeErr)
		case removeErr != nil:
			return nil, fmt.Errorf("chmod socket %s: %w (remove path: %v)", path, err, removeErr)
		default:
			return nil, fmt.Errorf("chmod socket %s: %w", path, err)
		}
	}
	return listener, nil
}
