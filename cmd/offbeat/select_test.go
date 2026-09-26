package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Iyed-M/offbeat/internal/acquisition"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/desired"
)

func TestCLIAcquireSelectAcknowledgesRejectionAndRecoversFailedWork(t *testing.T) {
	home, err := os.MkdirTemp("", "ob-cli-sel-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	tools := t.TempDir()
	_ = writeCLITool(t, tools, "yt-dlp", `printf '%s\n' '{"entries":[{"id":"abcdefghijk","title":"Artist - one","channel":"Artist","duration":1},{"id":"ZYXWvu_987-","title":"wrong video","channel":"Other","duration":1}]}'`)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	d, stop := startCLIAcquisitionDaemon(t, home, cliRetrieveFunc(func(context.Context, string) (*acquisition.Media, error) {
		return nil, errors.New("chosen video unavailable")
	}), cliResolveFunc(func(context.Context, desired.Track) (string, error) {
		return "", acquisition.ErrUnresolved
	}))
	defer stop()
	seedCLIAcquisitionTrack(t, d, "one")
	uri := "spotify:track:one"
	for _, args := range [][]string{{uri, "short"}, {uri, "https://youtu.be/abcdefghijk"}, {uri, "notInSearch"}} {
		out, stderr, err := runCLI(t, home, "acquire", append([]string{"select"}, args...)...)
		if err == nil || out != "" || stderr == "" {
			t.Fatalf("accepted %v: %q %q %v", args, out, stderr, err)
		}
	}
	out, stderr, err := runCLI(t, home, "acquire", "select", uri, "ZYXWvu_987-")
	if err == nil || out != "" || !strings.Contains(stderr, "--ack-rejection") {
		t.Fatalf("rejection: %q %q %v", out, stderr, err)
	}
	// Obtain the exact rejection identifier exposed by the inspection API.
	report, code := requestAcquisitionInspection("", home, uri, "test")
	if code != 0 {
		t.Fatalf("inspection exit %d", code)
	}
	reason := ""
	for _, candidate := range report.Candidates {
		if candidate.VideoID == "ZYXWvu_987-" {
			reason = string(candidate.RejectionReason)
		}
	}
	if reason == "" {
		t.Fatal("fixture did not reject candidate")
	}
	out, stderr, err = runCLI(t, home, "acquire", "select", uri, "abcdefghijk")
	if err != nil || stderr != "" {
		t.Fatalf("select: %q %q %v", out, stderr, err)
	}
	var result struct {
		ID int64 `json:"acquisition_id"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.ID != 1 {
		t.Fatalf("result: %q %v", out, err)
	}
	waitCLIAcquisitionState(t, d, result.ID, db.AcquisitionFailed)
	out, stderr, err = runCLI(t, home, "acquire", "select", uri, "ZYXWvu_987-", "--ack-rejection", reason)
	if err != nil || stderr != "" {
		t.Fatalf("replacement: %q %q %v", out, stderr, err)
	}
	work := waitCLIAcquisitionState(t, d, result.ID, db.AcquisitionFailed)
	if work.SourceURL != "https://www.youtube.com/watch?v=ZYXWvu_987-" {
		t.Fatalf("replacement source: %#v", work)
	}
	mapping, err := d.DB.ManualYouTubeMapping(context.Background(), uri)
	if err != nil || mapping.VideoID != "ZYXWvu_987-" {
		t.Fatalf("mapping: %#v %v", mapping, err)
	}
	works, err := d.DB.AcquisitionsAfter(context.Background(), 0, 10)
	if err != nil || len(works) != 1 {
		t.Fatalf("duplicate work: %#v %v", works, err)
	}
}
