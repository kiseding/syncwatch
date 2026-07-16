package room

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/kiseding/syncwatch/internal/media"
	"github.com/kiseding/syncwatch/internal/signaling"
)

func TestLocalMediaURLStateAndPosition(t *testing.T) {
	hub := signaling.NewHub()
	r := NewRoom(hub, t.TempDir())
	path := filepath.Join("media dir", "movie & one.mp4")
	r.SetMedia(path, SourceLocal)

	u, err := url.Parse(r.GetMediaURL())
	if err != nil {
		t.Fatal(err)
	}
	if u.IsAbs() || u.Path != "/api/media/file" || u.Query().Get("path") != path {
		t.Fatalf("bad local media URL: %q", r.GetMediaURL())
	}

	r.SetMediaInfo(&media.MediaInfo{Duration: 100}, []media.TrackInfo{{Type: "audio"}})
	r.Seek(150)
	if got := r.GetPosition(); got != 100 {
		t.Fatalf("position was not clamped: %v", got)
	}
	if err := r.SwitchAudioTrack(0); err != nil {
		t.Fatal(err)
	}
	if err := r.SwitchAudioTrack(1); err == nil {
		t.Fatal("out-of-range audio track was accepted")
	}
}

func TestMediaChangeClearsTracksAndSubtitleState(t *testing.T) {
	dir := t.TempDir()
	subPath := filepath.Join(dir, "movie.srt")
	if err := os.WriteFile(subPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nHello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewRoom(signaling.NewHub(), dir)
	r.SetMedia("first.mp4", SourceLocal)
	if err := r.AddSubtitle(media.SubtitleInfo{Path: subPath, Format: "srt"}); err != nil {
		t.Fatal(err)
	}
	r.SetMediaInfo(&media.MediaInfo{Duration: 10}, []media.TrackInfo{{Type: "audio"}})
	r.SetMedia("https://example.com/next.mp4", SourceURL)

	if len(r.GetAudioTracks()) != 0 || len(r.GetSubtitles()) != 0 || r.GetSubIndex() != -1 {
		t.Fatal("old media tracks leaked into the new media state")
	}
	if r.GetMediaInfo() != nil || r.GetMediaURL() != "https://example.com/next.mp4" {
		t.Fatal("new media state is incorrect")
	}
}
