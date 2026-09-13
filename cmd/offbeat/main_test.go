package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests are black-box: they build the real offbeatd and offbeat
// binaries and exercise them via exec.Command. TestMain builds the
// binaries once and exposes them as package-level paths.

var (
	offbeatdPath string
	offbeatPath  string
)

func TestMain(m *testing.M) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		_, _ = os.Stderr.WriteString("findRepoRoot: " + err.Error() + "\n")
		os.Exit(2)
	}

	dir, err := os.MkdirTemp("", "offbeat-cli-test-")
	if err != nil {
		_, _ = os.Stderr.WriteString("MkdirTemp: " + err.Error() + "\n")
		os.Exit(2)
	}
	defer os.RemoveAll(dir)

	offbeatdPath = filepath.Join(dir, "offbeatd")
	offbeatPath = filepath.Join(dir, "offbeat")

	if err := buildBinary(repoRoot, "./cmd/offbeatd", offbeatdPath); err != nil {
		_, _ = os.Stderr.WriteString("build offbeatd: " + err.Error() + "\n")
		os.Exit(2)
	}
	if err := buildBinary(repoRoot, "./cmd/offbeat", offbeatPath); err != nil {
		_, _ = os.Stderr.WriteString("build offbeat: " + err.Error() + "\n")
		os.Exit(2)
	}

	code := m.Run()
	os.Exit(code)
}

func TestCLIStatusAgainstRunningDaemon(t *testing.T) {
	home := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, offbeatdPath)
	cmd.Env = append(os.Environ(), "OFFBEAT_HOME="+home)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start offbeatd: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	}()

	if err := waitForDaemonReady(home, 5*time.Second); err != nil {
		t.Fatalf("daemon not ready: %v\nstderr:\n%s", err, stderr.String())
	}

	out, errOut, err := runCLI(t, home, "status")
	if err != nil {
		t.Fatalf("offbeat status: %v\nstderr:\n%s", err, errOut)
	}

	for _, want := range []string{
		"Offbeat daemon",
		"version       : 0.0.0-m1",
		"pid           :",
		"started_at    :",
		"database      : ready",
		"schema version 1",
		"socket        :",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\noutput:\n%s", want, out)
		}
	}
	if errOut != "" {
		// stderr should be empty on success
		t.Errorf("unexpected stderr: %q", errOut)
	}
}

func TestCLIStatusReportsDaemonUnavailableWhenNoDaemon(t *testing.T) {
	home := t.TempDir()

	out, errOut, err := runCLI(t, home, "status")
	if err == nil {
		t.Fatalf("expected error exit, got success\nstdout:\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code=%d want 1", exitErr.ExitCode())
	}
	if !strings.Contains(errOut, "daemon-unavailable") {
		t.Errorf("stderr missing 'daemon-unavailable': %q", errOut)
	}
	if out != "" {
		t.Errorf("expected empty stdout on failure, got: %q", out)
	}
}

func TestCLIStatusUnknownCommandExitsTwo(t *testing.T) {
	_, errOut, err := runCLI(t, t.TempDir(), "bogus")
	if err == nil {
		t.Fatal("expected error exit, got success")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code=%d want 2", exitErr.ExitCode())
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Errorf("stderr missing 'unknown command': %q", errOut)
	}
}

func runCLI(t *testing.T, home, subcommand string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(offbeatPath, "-home", home, subcommand)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func waitForDaemonReady(home string, timeout time.Duration) error {
	sockPath := filepath.Join(home, ".local", "state", "offbeat", "ipc", "offbeatd.sock")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("socket never appeared at " + sockPath)
}

func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("go.mod not found from " + wd)
}

func buildBinary(repoRoot, pkg, out string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return errors.New("go build " + pkg + ": " + err.Error() + "\n" + stderr.String())
	}
	return nil
}
