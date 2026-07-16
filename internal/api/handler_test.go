package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiseding/syncwatch/internal/room"
	"github.com/kiseding/syncwatch/internal/signaling"
)

func newTestHandler(t *testing.T) (*Handler, string, string) {
	t.Helper()
	mediaDir := filepath.Join(t.TempDir(), "media")
	uploadDir := filepath.Join(t.TempDir(), "uploads")
	if err := os.MkdirAll(mediaDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(room.NewRoom(signaling.NewHub(), uploadDir))
	h.ScanDirs = []string{mediaDir}
	h.UploadDir = uploadDir
	h.AllowedExts = []string{".mp4", ".webm"}
	h.FFprobePath = filepath.Join(t.TempDir(), "missing-ffprobe")
	return h, mediaDir, uploadDir
}

func TestPlayAndServeAllowedLocalMedia(t *testing.T) {
	h, mediaDir, _ := newTestHandler(t)
	mediaPath := filepath.Join(mediaDir, "movie & one.mp4")
	if err := os.WriteFile(mediaPath, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}

	body := strings.NewReader(`{"path":` + mustJSON(t, mediaPath) + `}`)
	rec := httptest.NewRecorder()
	h.Play(rec, httptest.NewRequest(http.MethodPost, "/api/playback/play", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("play failed: %d %s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	mediaURL := response["url"].(string)
	parsed, err := url.Parse(mediaURL)
	if err != nil || parsed.Query().Get("path") != mediaPath || parsed.IsAbs() {
		t.Fatalf("invalid media URL: %q", mediaURL)
	}

	serveRec := httptest.NewRecorder()
	serveReq := httptest.NewRequest(http.MethodGet, mediaURL, nil)
	serveReq.Header.Set("Range", "bytes=2-5")
	h.ServeFile(serveRec, serveReq)
	if serveRec.Code != http.StatusPartialContent || serveRec.Body.String() != "2345" {
		t.Fatalf("range response failed: %d %q", serveRec.Code, serveRec.Body.String())
	}
}

func TestRejectsFilesOutsideConfiguredDirectories(t *testing.T) {
	h, _, _ := newTestHandler(t)
	outside := filepath.Join(t.TempDir(), "private.mp4")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeFile(rec, httptest.NewRequest(http.MethodGet, "/api/media/file?"+url.Values{"path": {outside}}.Encode(), nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("outside file returned %d", rec.Code)
	}

	sibling := h.ScanDirs[0] + "-other"
	if err := os.MkdirAll(sibling, 0755); err != nil {
		t.Fatal(err)
	}
	if h.pathWithinAllowedDirs(sibling, false) {
		t.Fatal("prefix-sibling directory passed the boundary check")
	}
}

func TestUploadMediaAndSubtitle(t *testing.T) {
	h, _, uploadDir := newTestHandler(t)

	mediaRec := httptest.NewRecorder()
	h.Upload(mediaRec, multipartRequest(t, "/api/upload", "file", "clip.mp4", []byte("video")))
	if mediaRec.Code != http.StatusOK {
		t.Fatalf("upload failed: %d %s", mediaRec.Code, mediaRec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(uploadDir, "clip.mp4")); err != nil {
		t.Fatal(err)
	}

	secondRec := httptest.NewRecorder()
	h.Upload(secondRec, multipartRequest(t, "/api/upload", "file", "clip.mp4", []byte("second")))
	if _, err := os.Stat(filepath.Join(uploadDir, "clip-2.mp4")); err != nil {
		t.Fatal("duplicate upload should not overwrite the first file")
	}

	subtitle := []byte("1\n00:00:00,000 --> 00:00:01,000\nHello\n")
	subRec := httptest.NewRecorder()
	h.UploadSubtitle(subRec, multipartRequest(t, "/api/upload/subtitle", "file", "clip.srt", subtitle))
	if subRec.Code != http.StatusOK {
		t.Fatalf("subtitle upload failed: %d %s", subRec.Code, subRec.Body.String())
	}
	format, content, index := h.Room.GetSubtitleData()
	if format != "ass" || !strings.Contains(content, "Hello") || index < 0 {
		t.Fatalf("subtitle state not retained: %q %q %d", format, content, index)
	}
}

func TestMediaScanIsRestrictedAndSorted(t *testing.T) {
	h, mediaDir, _ := newTestHandler(t)
	for _, name := range []string{"b.mp4", "A.webm", "ignore.txt"} {
		if err := os.WriteFile(filepath.Join(mediaDir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	h.MediaScan(rec, httptest.NewRequest(http.MethodGet, "/api/media/scan", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scan failed: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 2 || !strings.HasSuffix(result.Files[0].Path, "A.webm") {
		t.Fatalf("unexpected scan result: %#v", result.Files)
	}

	outsideRec := httptest.NewRecorder()
	h.MediaScan(outsideRec, httptest.NewRequest(http.MethodGet, "/api/media/scan?"+url.Values{"dir": {t.TempDir()}}.Encode(), nil))
	if outsideRec.Code != http.StatusForbidden {
		t.Fatalf("outside scan returned %d", outsideRec.Code)
	}
}

func TestPlaybackControlsRequireMedia(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rec := httptest.NewRecorder()
	h.Resume(rec, httptest.NewRequest(http.MethodPost, "/api/playback/resume", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("resume without media returned %d", rec.Code)
	}
}

func multipartRequest(t *testing.T, target, field, name string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(field, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(part, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
