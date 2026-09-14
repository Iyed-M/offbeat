package app

import (
	"bufio"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// === Unit tests for lifecycle primitives ===

func TestAcquireLockSucceedsWhenFree(t *testing.T) {
	stateDir := t.TempDir()
	lock, err := AcquireLock(stateDir)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	info, err := os.Stat(LockPath(stateDir))
	if err != nil {
		t.Fatalf("lock file missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock file mode = %#o, want 0600", info.Mode().Perm())
	}
}

func TestAcquireLockFailsWhenHeld(t *testing.T) {
	stateDir := t.TempDir()
	a, err := AcquireLock(stateDir)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	t.Cleanup(func() { _ = a.Release() })

	_, err = AcquireLock(stateDir)
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld, got %v", err)
	}
}

func TestAcquireLockSucceedsAfterRelease(t *testing.T) {
	stateDir := t.TempDir()
	a, err := AcquireLock(stateDir)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := a.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	b, err := AcquireLock(stateDir)
	if err != nil {
		t.Fatalf("second AcquireLock: %v", err)
	}
	t.Cleanup(func() { _ = b.Release() })
}

func TestEnforceSocketDirCreates0700(t *testing.T) {
	dir := t.TempDir()
	socketDir := filepath.Join(dir, "ipc")
	if err := EnforceSocketDirPermissions(socketDir); err != nil {
		t.Fatalf("EnforceSocketDirPermissions: %v", err)
	}
	info, err := os.Stat(socketDir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir mode = %#o, want 0700", info.Mode().Perm())
	}
}

func TestEnforceSocketDirRejectsLoosePerms(t *testing.T) {
	dir := t.TempDir()
	socketDir := filepath.Join(dir, "ipc")
	if err := os.MkdirAll(socketDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := EnforceSocketDirPermissions(socketDir); err == nil {
		t.Fatal("expected error for 0755 socket dir, got nil")
	} else if !errors.Is(err, ErrSocketDirPermissions) {
		t.Fatalf("expected ErrSocketDirPermissions, got %v", err)
	}
	info, _ := os.Stat(socketDir)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("perm was changed to %#o", info.Mode().Perm())
	}
}

func TestResolveStaleSocketNoEntry(t *testing.T) {
	dir := t.TempDir()
	socketDir := filepath.Join(dir, "ipc")
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale, err := ResolveStaleSocket(socketDir)
	if err != nil {
		t.Fatalf("ResolveStaleSocket: %v", err)
	}
	if stale {
		t.Fatal("expected no stale socket")
	}
}

func TestResolveStaleSocketRecoversStaleSocket(t *testing.T) {
	dir := t.TempDir()
	socketDir := filepath.Join(dir, "ipc")
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sockPath := SocketPath(socketDir)

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	} else {
		t.Fatal("listener is not *net.UnixListener")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("stale socket not present: %v", err)
	}

	stale, err := ResolveStaleSocket(socketDir)
	if err != nil {
		t.Fatalf("ResolveStaleSocket: %v", err)
	}
	if !stale {
		t.Fatal("expected stale=true")
	}
	if _, err := os.Stat(sockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale socket not removed: %v", err)
	}
}

func TestResolveStaleSocketRefusesRegularFile(t *testing.T) {
	dir := t.TempDir()
	socketDir := filepath.Join(dir, "ipc")
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sockPath := SocketPath(socketDir)
	if err := os.WriteFile(sockPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveStaleSocket(socketDir)
	if !errors.Is(err, ErrNonSocketAtSocketPath) {
		t.Fatalf("expected ErrNonSocketAtSocketPath, got %v", err)
	}
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("non-socket entry was deleted: %v", err)
	}
}

func TestResolveStaleSocketRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	socketDir := filepath.Join(dir, "ipc")
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sockPath := SocketPath(socketDir)
	target := filepath.Join(t.TempDir(), "elsewhere.sock")
	if err := os.Symlink(target, sockPath); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveStaleSocket(socketDir)
	if !errors.Is(err, ErrNonSocketAtSocketPath) {
		t.Fatalf("expected ErrNonSocketAtSocketPath, got %v", err)
	}
	if _, err := os.Lstat(sockPath); err != nil {
		t.Fatalf("symlink was deleted: %v", err)
	}
}

func TestBindControlSocketSetsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	socketDir := filepath.Join(dir, "ipc")
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := BindControlSocket(socketDir)
	if err != nil {
		t.Fatalf("BindControlSocket: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(SocketPath(socketDir))
	})
	info, err := os.Stat(SocketPath(socketDir))
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %#o, want 0600", info.Mode().Perm())
	}
}

// === Subprocess helpers ===

type helperHandle struct {
	cmd   *exec.Cmd
	lines chan string
}

func startHelper(t *testing.T, homeDir string) *helperHandle {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonHelperProcess$")
	cmd.Env = append(os.Environ(),
		"OFFBEAT_DAEMON_HELPER=1",
		"OFFBEAT_HOME="+homeDir,
		"OFFBEAT_ADAPTER_CREDENTIAL="+testAdapterCredential,
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return &helperHandle{cmd: cmd, lines: lines}
}

func (h *helperHandle) waitForLog(t *testing.T, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-h.lines:
			if !ok {
				t.Fatalf("helper stderr closed before %q", substr)
			}
			if strings.Contains(line, substr) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q", substr)
		}
	}
}

func (h *helperHandle) drainLogs() {
	go func() {
		for range h.lines {
		}
	}()
}

func (h *helperHandle) waitExit(t *testing.T, timeout time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- h.cmd.Wait() }()
	deadline := time.After(timeout)
	for {
		select {
		case <-h.lines:
			// discard
		case err := <-done:
			return err
		case <-deadline:
			_ = h.cmd.Process.Kill()
			t.Fatalf("helper did not exit within %s", timeout)
		}
	}
}

// === Subprocess integration tests ===

func TestDaemonStartsAndExitsCleanlyOnSIGTERM(t *testing.T) {
	homeDir := t.TempDir()
	h := startHelper(t, homeDir)
	defer func() {
		_ = h.cmd.Process.Kill()
		_ = h.cmd.Wait()
	}()
	h.waitForLog(t, "offbeatd ready", 5*time.Second)
	if err := h.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	h.drainLogs()
	if err := h.waitExit(t, 5*time.Second); err != nil {
		t.Fatalf("helper did not exit cleanly: %v", err)
	}
}

func helperSocketDir(homeDir string) string {
	return filepath.Join(homeDir, ".local", "state", "offbeat", "ipc")
}

func TestConcurrentStartSecondFails(t *testing.T) {
	homeDir := t.TempDir()

	a := startHelper(t, homeDir)
	a.waitForLog(t, "offbeatd ready", 5*time.Second)
	defer func() {
		_ = a.cmd.Process.Signal(syscall.SIGTERM)
		a.drainLogs()
		_ = a.cmd.Wait()
	}()

	b := startHelper(t, homeDir)
	defer func() {
		_ = b.cmd.Process.Kill()
		_ = b.cmd.Wait()
	}()
	waitForFailure(t, b, 5*time.Second)

	if _, err := os.Stat(SocketPath(helperSocketDir(homeDir))); err != nil {
		t.Fatalf("incumbent socket missing after competitor failed: %v", err)
	}

	conn, err := net.Dial("unix", SocketPath(helperSocketDir(homeDir)))
	if err != nil {
		t.Fatalf("incumbent socket unreachable after competitor failed: %v", err)
	}
	_ = conn.Close()
}

func TestStaleSocketRecovery(t *testing.T) {
	homeDir := t.TempDir()
	socketDir := helperSocketDir(homeDir)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sockPath := SocketPath(socketDir)

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	} else {
		t.Fatal("listener is not *net.UnixListener")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("stale socket not present: %v", err)
	}

	h := startHelper(t, homeDir)
	defer func() {
		_ = h.cmd.Process.Kill()
		_ = h.cmd.Wait()
	}()
	h.waitForLog(t, "recovered stale control socket", 5*time.Second)
	h.waitForLog(t, "offbeatd ready", 5*time.Second)

	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("socket gone after recovery: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %#o, want 0600 after recovery", info.Mode().Perm())
	}
}

func TestNonSocketAtSocketPathAborts(t *testing.T) {
	homeDir := t.TempDir()
	socketDir := helperSocketDir(homeDir)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sockPath := SocketPath(socketDir)
	if err := os.WriteFile(sockPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := os.Stat(sockPath)
		if err != nil {
			t.Fatalf("non-socket entry was deleted by startup: %v", err)
		}
	})

	h := startHelper(t, homeDir)
	defer func() { _ = h.cmd.Wait() }()
	waitForFailure(t, h, 5*time.Second)
}

func TestReplacementOwner(t *testing.T) {
	homeDir := t.TempDir()

	a := startHelper(t, homeDir)
	a.waitForLog(t, "offbeatd ready", 5*time.Second)
	if err := a.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM A: %v", err)
	}
	a.drainLogs()
	if err := a.waitExit(t, 5*time.Second); err != nil {
		t.Fatalf("A did not exit cleanly: %v", err)
	}

	if _, err := os.Stat(SocketPath(helperSocketDir(homeDir))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket not removed by A: err=%v", err)
	}

	b := startHelper(t, homeDir)
	defer func() {
		_ = b.cmd.Process.Kill()
		_ = b.cmd.Wait()
	}()
	b.waitForLog(t, "offbeatd ready", 5*time.Second)
}

func TestCrashRecoveryAfterSIGKILL(t *testing.T) {
	homeDir := t.TempDir()
	socketDir := helperSocketDir(homeDir)

	a := startHelper(t, homeDir)
	a.waitForLog(t, "offbeatd ready", 5*time.Second)

	if err := a.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL A: %v", err)
	}
	if err := a.cmd.Wait(); err != nil {
		t.Logf("A exit status after SIGKILL: %v", err)
	}
	a.drainLogs()

	if _, err := os.Stat(SocketPath(socketDir)); err != nil {
		t.Fatalf("stale socket not present after SIGKILL: %v", err)
	}

	b := startHelper(t, homeDir)
	defer func() {
		_ = b.cmd.Process.Kill()
		_ = b.cmd.Wait()
	}()
	b.waitForLog(t, "recovered stale control socket", 5*time.Second)
	b.waitForLog(t, "offbeatd ready", 5*time.Second)

	conn, err := net.Dial("unix", SocketPath(socketDir))
	if err != nil {
		t.Fatalf("replacement socket unreachable: %v", err)
	}
	_ = conn.Close()
}

func TestSocketDirPermissionsRefuseStartup(t *testing.T) {
	homeDir := t.TempDir()
	socketDir := helperSocketDir(homeDir)
	if err := os.MkdirAll(socketDir, 0o755); err != nil {
		t.Fatal(err)
	}

	h := startHelper(t, homeDir)
	defer func() { _ = h.cmd.Wait() }()
	waitForFailure(t, h, 5*time.Second)
}

func TestSocketAndDirAreOwnerOnly(t *testing.T) {
	homeDir := t.TempDir()
	socketDir := helperSocketDir(homeDir)

	h := startHelper(t, homeDir)
	defer func() {
		_ = h.cmd.Process.Kill()
		_ = h.cmd.Wait()
	}()
	h.waitForLog(t, "offbeatd ready", 5*time.Second)

	dirInfo, err := os.Stat(socketDir)
	if err != nil {
		t.Fatalf("stat socket dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir mode = %#o, want 0700", dirInfo.Mode().Perm())
	}
	sockInfo, err := os.Stat(SocketPath(socketDir))
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if sockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %#o, want 0600", sockInfo.Mode().Perm())
	}
}

func TestShutdownOrderIsRespected(t *testing.T) {
	homeDir := t.TempDir()
	h := startHelper(t, homeDir)
	defer func() {
		_ = h.cmd.Process.Kill()
		_ = h.cmd.Wait()
	}()
	h.waitForLog(t, "offbeatd ready", 5*time.Second)

	if err := h.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}

	// Each subsequent phase must arrive on stderr in order, so waitForLog in
	// sequence is itself the ordering check.
	h.waitForLog(t, "phase=stop_accept", 5*time.Second)
	h.waitForLog(t, "phase=drained", 5*time.Second)
	h.waitForLog(t, "phase=remove_socket", 5*time.Second)
	h.waitForLog(t, "offbeatd stopped", 5*time.Second)

	if _, err := os.Stat(SocketPath(helperSocketDir(homeDir))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still present after shutdown: err=%v", err)
	}
}

// waitForFailure consumes helper stderr until the subprocess exits and fails the
// test if the subprocess exited cleanly or did not exit within timeout.
func waitForFailure(t *testing.T, h *helperHandle, timeout time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- h.cmd.Wait() }()
	deadline := time.After(timeout)
	var sawLockHeld bool
	for {
		select {
		case line, ok := <-h.lines:
			if !ok {
				_ = <-done
				return
			}
			if strings.Contains(line, "another daemon owner holds the lock") {
				sawLockHeld = true
			}
		case err := <-done:
			if err == nil && !sawLockHeld {
				t.Fatalf("helper exited cleanly when startup was expected to fail")
			}
			return
		case <-deadline:
			_ = h.cmd.Process.Kill()
			t.Fatalf("helper did not exit within %s", timeout)
		}
	}
}
