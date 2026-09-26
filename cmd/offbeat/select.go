package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func runAcquireSelect(configPath, homeDir string, args []string) int {
	const usage = "offbeat: usage: offbeat acquire select <spotify-track-uri> <youtube-video-id> [--ack-rejection <reason>]"
	if (len(args) != 2 && len(args) != 4) || (len(args) == 4 && args[2] != "--ack-rejection") {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	uri, id := args[0], args[1]
	if err := ipc.ValidateManualMappingTrackURI(uri); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := ipc.ValidateYouTubeVideoID(id); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ack := ""
	if len(args) == 4 {
		ack = args[3]
	}
	report, code := requestAcquisitionInspection(configPath, homeDir, uri, "acquire select")
	if code != 0 {
		return code
	}
	c := &ipc.AcquisitionChoice{TrackURI: uri, VideoID: id, ExpectedTitle: report.Track.Title, ExpectedDurationMS: report.Track.DurationMS, ExpectedAlbum: report.Track.Album.Name, ExpectedAlbumURI: report.Track.Album.URI}
	for _, artist := range report.Track.Artists {
		c.ExpectedArtists = append(c.ExpectedArtists, artist.Name)
		c.ExpectedArtistURIs = append(c.ExpectedArtistURIs, artist.URI)
	}
	found := false
	for _, candidate := range report.Candidates {
		if candidate.VideoID != id {
			continue
		}
		found = true
		c.RejectionReason = string(candidate.RejectionReason)
		break
	}
	if !found {
		fmt.Fprintln(os.Stderr, "offbeat acquire select: video ID not in fresh inspection")
		return 1
	}
	if c.RejectionReason != "" && ack != c.RejectionReason {
		fmt.Fprintf(os.Stderr, "offbeat acquire select: candidate rejected: %s; repeat with --ack-rejection %s to deliberately override\n", c.RejectionReason, c.RejectionReason)
		return 1
	}
	if c.RejectionReason == "" && ack != "" {
		fmt.Fprintln(os.Stderr, "offbeat acquire select: candidate is eligible; no rejection acknowledgment needed")
		return 2
	}
	c.AcknowledgeRejection = ack
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire select: config: %v\n", err)
		return 1
	}
	resp, err := requestControlMessage(app.SocketPath(bootstrap.SocketDir), ipc.Request{Version: ipc.ProtocolVersion, Command: "acquire.select", AcquisitionChoice: c}, youtubeInspectReadTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire select: daemon-unavailable: %v\n", err)
		return 1
	}
	if resp.Error != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire select: %s: %s\n", resp.Error.Code, resp.Error.Message)
		return 1
	}
	if resp.Version != ipc.ProtocolVersion {
		fmt.Fprintln(os.Stderr, "offbeat acquire select: unexpected daemon reply")
		return 1
	}
	work, err := decodeAcquisitionResult(resp.Result)
	if err != nil || work.TrackURI != uri || work.SourceKind != "youtube" {
		fmt.Fprintln(os.Stderr, "offbeat acquire select: unexpected daemon reply")
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(work); err != nil {
		fmt.Fprintf(os.Stderr, "offbeat acquire select: output: %v\n", err)
		return 1
	}
	return 0
}
