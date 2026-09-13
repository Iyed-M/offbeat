package domain

import "testing"

func TestSpotifyTrackIDIsZero(t *testing.T) {
	if !(SpotifyTrackID("")).IsZero() {
		t.Fatal("empty should be zero")
	}
	if (SpotifyTrackID("abc")).IsZero() {
		t.Fatal("non-empty should not be zero")
	}
}

func TestParseAcquisitionStateValid(t *testing.T) {
	for _, s := range []string{
		"pending", "resolving", "downloading", "normalizing",
		"ready", "failed_temporary", "unresolved", "failed_permanent", "review_required",
	} {
		st, err := ParseAcquisitionState(s)
		if err != nil {
			t.Fatalf("ParseAcquisitionState(%q): %v", s, err)
		}
		if string(st) != s {
			t.Fatalf("roundtrip mismatch: %q", st)
		}
	}
}

func TestParseAcquisitionStateInvalid(t *testing.T) {
	if _, err := ParseAcquisitionState("nonsense"); err == nil {
		t.Fatal("expected error for invalid state")
	}
}

func TestPlaylistPreservesOrder(t *testing.T) {
	p := Playlist{
		ID:       "p1",
		TrackIDs: []SpotifyTrackID{"a", "b", "c", "a"},
	}
	if len(p.TrackIDs) != 4 || p.TrackIDs[3] != "a" {
		t.Fatalf("order/duplicates lost: %v", p.TrackIDs)
	}
}

func TestSnapshotIDZero(t *testing.T) {
	if !(SnapshotID(0)).IsZero() {
		t.Fatal("0 should be zero")
	}
	if (SnapshotID(1)).IsZero() {
		t.Fatal("1 should not be zero")
	}
}
