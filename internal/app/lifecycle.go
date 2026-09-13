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

func AcquireLock(stateDir string) (*Lock, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir state dir: %w", err)
	}
	path := LockPath(stateDir)
	fd, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = fd.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return &Lock{fd: fd, path: path}, nil
}

func (l *Lock) Release() error {
	if l == nil || l.fd == nil {
		return nil
	}
	_ = syscall.Flock(int(l.fd.Fd()), syscall.LOCK_UN)
	err := l.fd.Close()
	l.fd = nil
	return err
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
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("chmod socket %s: %w", path, err)
	}
	return listener, nil
}
