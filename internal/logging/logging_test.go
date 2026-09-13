package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in      string
		want    Level
		wantErr bool
	}{
		{"debug", LevelDebug, false},
		{"INFO", LevelInfo, false},
		{"", LevelInfo, false},
		{"warn", LevelWarn, false},
		{"warning", LevelWarn, false},
		{"error", LevelError, false},
		{"trace", LevelInfo, true},
	}
	for _, c := range cases {
		got, err := ParseLevel(c.in)
		if (err != nil) != c.wantErr {
			t.Fatalf("ParseLevel(%q) err=%v wantErr=%v", c.in, err, c.wantErr)
		}
		if !c.wantErr && got != c.want {
			t.Fatalf("ParseLevel(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in      string
		want    Format
		wantErr bool
	}{
		{"", FormatText, false},
		{"text", FormatText, false},
		{"JSON", FormatJSON, false},
		{"yaml", FormatText, true},
	}
	for _, c := range cases {
		got, err := ParseFormat(c.in)
		if (err != nil) != c.wantErr {
			t.Fatalf("ParseFormat(%q) err=%v wantErr=%v", c.in, err, c.wantErr)
		}
		if !c.wantErr && got != c.want {
			t.Fatalf("ParseFormat(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func mustLogger(t *testing.T, cfg Config) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	cfg.Writer = &buf
	logger, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return logger, &buf
}

func TestNewJSONLogger(t *testing.T) {
	logger, buf := mustLogger(t, Config{Level: LevelInfo, Format: FormatJSON})
	logger.Info("hello", "k", "v")
	if !strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Fatalf("missing msg in JSON output: %q", buf.String())
	}
	if !strings.Contains(buf.String(), `"k":"v"`) {
		t.Fatalf("missing kv in JSON output: %q", buf.String())
	}
}

func TestNewTextLogger(t *testing.T) {
	logger, buf := mustLogger(t, Config{Level: LevelDebug, Format: FormatText})
	logger.Debug("hi")
	if !strings.Contains(buf.String(), "hi") {
		t.Fatalf("expected hi in text output: %q", buf.String())
	}
}

func TestNewLevelFiltering(t *testing.T) {
	logger, buf := mustLogger(t, Config{Level: LevelWarn, Format: FormatJSON})
	logger.Info("should-be-filtered")
	logger.Warn("should-appear")
	out := buf.String()
	if strings.Contains(out, "should-be-filtered") {
		t.Fatalf("info message should be filtered at warn level: %s", out)
	}
	if !strings.Contains(out, "should-appear") {
		t.Fatalf("expected warn message in output: %s", out)
	}
}

func TestLoggerIsJSON(t *testing.T) {
	logger, buf := mustLogger(t, Config{Level: LevelInfo, Format: FormatJSON})
	logger.Info("x")
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &m); err != nil {
		t.Fatalf("not valid JSON: %v: %q", err, buf.String())
	}
	if m["msg"] != "x" {
		t.Fatalf("msg=%v", m["msg"])
	}
}

func TestNewWithFile(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "offbeatd.log")
	logger, err := New(Config{Level: LevelInfo, Format: FormatJSON, File: logPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Info("file-test", "k", "v")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), `"msg":"file-test"`) {
		t.Fatalf("expected msg in log file: %q", data)
	}
}

func TestNewWithBadFile(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "missing-dir", "x.log")
	if _, err := New(Config{Level: LevelInfo, Format: FormatJSON, File: bad}); err == nil {
		t.Fatal("expected error opening log file in missing directory")
	}
}
