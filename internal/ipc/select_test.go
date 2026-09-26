package ipc

import "testing"

func TestAcquireSelectWireRequiresValidChoiceAndExactAcknowledgment(t *testing.T) {
	valid := `{"version":1,"command":"acquire.select","acquisition_choice":{"track_uri":"spotify:track:one","video_id":"abcdefghijk","expected_title":"one","expected_artists":["Artist"],"expected_artist_uris":["spotify:artist:one"],"expected_duration_ms":1000,"rejection_reason":"metadata_mismatch","acknowledge_rejection":"metadata_mismatch"}}`
	var request Request
	if err := Decode([]byte(valid), &request); err != nil || request.AcquisitionChoice == nil {
		t.Fatalf("valid selection: %#v %v", request, err)
	}
	for _, raw := range []string{
		`{"version":1,"command":"acquire.select"}`,
		`{"version":1,"command":"acquire.select","acquisition_choice":null}`,
		`{"version":1,"command":"acquire.select","acquisition_choice":{"track_uri":"spotify:track:one","video_id":"https://youtu.be/abcdefghijk"}}`,
		`{"version":1,"command":"acquire.inspect","acquisition_choice":{}}`,
		`{"version":1,"command":"acquire.select","acquisition_choice":{"track_uri":"spotify:track:one","video_id":"abcdefghijk","expected_title":"one","expected_artists":["Artist"],"expected_artist_uris":["spotify:artist:one"],"expected_duration_ms":1000,"rejection_reason":"metadata_mismatch","acknowledge_rejection":"different"}}`,
	} {
		if err := Decode([]byte(raw), &request); err == nil {
			t.Errorf("accepted malformed selection: %s", raw)
		}
	}
}
