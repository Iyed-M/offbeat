package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/desired"
)

func TestCLIMappingCRUDAndFailedChoice(t *testing.T) {
	home := t.TempDir()
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		return nil, errors.New("chosen video unavailable")
	}), cliResolveFunc(func(context.Context, desired.Track) (string, error) {
		return "", errors.New("resolver should not run")
	}))
	defer stop()
	seedCLIAcquisitionTrack(t, d, "one")
	uri := "spotify:track:one"
	for _, args := range [][]string{{"set", uri, "abcdefghijk"}, {"show", uri}, {"list"}} {
		out, stderr, err := runCLI(t, home, "acquire", append([]string{"mapping"}, args...)...)
		if err != nil || stderr != "" || !strings.Contains(out, uri+" -> abcdefghijk (manual;") {
			t.Fatalf("%v: %q %q %v", args, out, stderr, err)
		}
	}
	out, stderr, err := runCLI(t, home, "acquire", "missing")
	if err != nil || stderr != "" || !strings.Contains(out, "1 queued") {
		t.Fatalf("missing: %q %q %v", out, stderr, err)
	}
	waitCLIAcquisitionState(t, d, 1, "failed")
	out, stderr, err = runCLI(t, home, "acquire", "mapping", "show", uri)
	if err != nil || stderr != "" || !strings.Contains(out, "chosen video unavailable") {
		t.Fatalf("failed choice: %q %q %v", out, stderr, err)
	}
	out, stderr, err = runCLI(t, home, "acquire", "mapping", "set", uri, "ZYXWvu_987-")
	if err != nil || stderr != "" || !strings.Contains(out, "ZYXWvu_987-") {
		t.Fatalf("replace: %q %q %v", out, stderr, err)
	}
	out, stderr, err = runCLI(t, home, "acquire", "mapping", "remove", uri)
	if err != nil || stderr != "" || out != fmt.Sprintf("Removed mapping for %s.\n", uri) {
		t.Fatalf("remove: %q %q %v", out, stderr, err)
	}
	out, stderr, err = runCLI(t, home, "acquire", "mapping", "list")
	if err != nil || stderr != "" || out != "" {
		t.Fatalf("empty list: %q %q %v", out, stderr, err)
	}
	for _, args := range [][]string{{"set", uri, "https://youtu.be/abcdefghijk"}, {"set", uri, "short"}, {"show", "spotify:track:one:extra"}} {
		out, stderr, err := runCLI(t, home, "acquire", append([]string{"mapping"}, args...)...)
		if err == nil || out != "" || stderr == "" {
			t.Fatalf("accepted %v: %q %q %v", args, out, stderr, err)
		}
	}
}
