package logging

import (
	"bytes"
	"encoding/json"
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

func TestNewJSONLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{Level: LevelInfo, Format: FormatJSON, Writer: &buf})
	logger.Info("hello", "k", "v")
	out := buf.String()
	if !strings.Contains(out, `"msg":"hello"`) {
		t.Fatalf("missing msg in JSON output: %q", out)
	}
	if !strings.Contains(out, `"k":"v"`) {
		t.Fatalf("missing kv in JSON output: %q", out)
	}
}

func TestNewTextLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{Level: LevelDebug, Format: FormatText, Writer: &buf})
	logger.Debug("hi")
	if !strings.Contains(buf.String(), "hi") {
		t.Fatalf("expected hi in text output: %q", buf.String())
	}
}

func TestNewLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Config{Level: LevelWarn, Format: FormatJSON, Writer: &buf})
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
	var buf bytes.Buffer
	logger := New(Config{Level: LevelInfo, Format: FormatJSON, Writer: &buf})
	logger.Info("x")
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &m); err != nil {
		t.Fatalf("not valid JSON: %v: %q", err, buf.String())
	}
	if m["msg"] != "x" {
		t.Fatalf("msg=%v", m["msg"])
	}
}
