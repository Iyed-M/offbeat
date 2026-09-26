package app

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func TestReviewListCurrentMissingWorkOverMultiplePages(t *testing.T) {
	home, err := os.MkdirTemp("", "ob-review-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	d := acquisitionDaemon(t, home, nil, 1)
	ids := make([]string, 105)
	for i := range ids {
		ids[i] = fmt.Sprintf("track%03d", i)
	}
	seedAcquisitionTracks(t, d, ids...)
	ctx := context.Background()
	for i, id := range ids {
		state := db.AcquisitionUnresolved
		if i == 1 {
			state = db.AcquisitionFailed
		}
		if i == 2 {
			state = db.AcquisitionPending
		}
		if _, err := d.DB.ExecContext(ctx, `INSERT INTO acquisition_work(track_uri, source_kind, source_url, state, error, created_at, updated_at) VALUES (?, 'youtube', NULL, ?, '', '', '')`, "spotify:track:"+id, state); err != nil {
			t.Fatal(err)
		}
	}
	path, err := d.managedFiles.PublishSynthetic("spotify:track:track003")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DB.RegisterManagedTrack(ctx, "spotify:track:track003", path); err != nil {
		t.Fatal(err)
	}
	var seen []ipc.ReviewTrack
	after := ""
	for {
		req := ipc.Request{Command: "review.list"}
		if after != "" {
			req.ReviewList = &ipc.ReviewListRequest{AfterURI: after}
		}
		result, err := d.handleControlRequest(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		page := result.(ipc.ReviewPage)
		if len(page.Tracks) > reviewPageSize {
			t.Fatalf("unbounded page: %d", len(page.Tracks))
		}
		seen = append(seen, page.Tracks...)
		if page.NextAfterURI == "" {
			break
		}
		if page.NextAfterURI <= after {
			t.Fatalf("non-advancing cursor: %s", page.NextAfterURI)
		}
		after = page.NextAfterURI
	}
	if len(seen) != 103 || seen[1].TrackURI != "spotify:track:track001" || seen[1].WorkState != db.AcquisitionFailed || seen[len(seen)-1].TrackURI != "spotify:track:track104" {
		t.Fatalf("review rows: %d, first: %#v, last: %#v", len(seen), seen[:min(4, len(seen))], seen[len(seen)-1])
	}
	counts, err := d.DB.AcquisitionCounts(ctx)
	if err != nil || counts.Unresolved != 103 || counts.Failed != 1 || counts.Pending != 1 {
		t.Fatalf("GET-like listing changed work: %#v %v", counts, err)
	}
}
