package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kiseding/syncwatch/internal/media"
	"github.com/kiseding/syncwatch/internal/room"
	"github.com/kiseding/syncwatch/internal/signaling"
)

// Handler handles HTTP API requests.
type Handler struct {
	Room        *room.Room
	AllowedExts []string
	UploadDir   string
	FFprobePath string
	ScanDirs    []string // configured media scan directories
}

// NewHandler creates a new API handler.
func NewHandler(r *room.Room) *Handler {
	return &Handler{Room: r}
}

// ---- Playback ----

// Play sets the media source and starts playback.
func (h *Handler) Play(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	var sourceType room.SourceType
	parsedURL, urlErr := url.Parse(req.Path)
	if urlErr == nil && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") && parsedURL.Host != "" {
		sourceType = room.SourceURL
	} else {
		cleanPath, err := h.allowedLocalFile(req.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Path = cleanPath
		sourceType = room.SourceLocal
	}

	h.Room.SetMedia(req.Path, sourceType)

	// Probe media in background
	go func() {
		info, err := media.Probe(h.FFprobePath, req.Path)
		if err == nil && h.Room.IsCurrentMedia(req.Path) {
			var audioTracks []media.TrackInfo
			for _, t := range info.Tracks {
				if t.Type == "audio" {
					audioTracks = append(audioTracks, t)
				}
			}
			h.Room.SetMediaInfo(info, audioTracks)
		}

		// Detect subtitles
		subs, _ := media.ExtractSubtitles("ffmpeg", req.Path)
		if len(subs) > 0 && h.Room.IsCurrentMedia(req.Path) {
			h.Room.SetSubtitles(subs)
		}
	}()
	url := h.Room.GetMediaURL()

	// Broadcast media URL change so viewers load the new source
	h.Room.Hub().Broadcast(signaling.Message{
		Type:     signaling.MsgMedia,
		MediaURL: url,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "playing",
		"url":    url,
	})
}

// Pause pauses playback.
func (h *Handler) Pause(w http.ResponseWriter, r *http.Request) {
	if !h.requireMedia(w) {
		return
	}
	h.Room.Pause()
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// Resume resumes playback.
func (h *Handler) Resume(w http.ResponseWriter, r *http.Request) {
	if !h.requireMedia(w) {
		return
	}
	h.Room.Resume()
	writeJSON(w, http.StatusOK, map[string]string{"status": "playing"})
}

// Seek seeks to a position.
func (h *Handler) Seek(w http.ResponseWriter, r *http.Request) {
	if !h.requireMedia(w) {
		return
	}
	var req struct {
		Position float64 `json:"position"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	h.Room.Seek(req.Position)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "playing",
		"position": h.Room.GetPosition(),
	})
}

// SetSpeed changes playback speed.
func (h *Handler) SetSpeed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Speed float64 `json:"speed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	validSpeeds := map[float64]bool{0.5: true, 1.0: true, 1.25: true, 1.5: true, 2.0: true}
	if !validSpeeds[req.Speed] {
		writeError(w, http.StatusBadRequest, "speed must be one of: 0.5, 1.0, 1.25, 1.5, 2.0")
		return
	}
	h.Room.SetSpeed(req.Speed)
	writeJSON(w, http.StatusOK, map[string]interface{}{"speed": req.Speed})
}

// SyncPosition updates playback position from host's periodic sync.
func (h *Handler) SyncPosition(w http.ResponseWriter, r *http.Request) {
	if !h.requireMedia(w) {
		return
	}
	var req struct {
		Position float64 `json:"position"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	h.Room.SyncPosition(req.Position)
	w.WriteHeader(http.StatusNoContent)
}

// SwitchAudio switches audio track.
func (h *Handler) SwitchAudio(w http.ResponseWriter, r *http.Request) {
	if !h.requireMedia(w) {
		return
	}
	var req struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.Room.SwitchAudioTrack(req.Index); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"audio_index": req.Index})
}

// SwitchSubtitle switches subtitle track.
func (h *Handler) SwitchSubtitle(w http.ResponseWriter, r *http.Request) {
	if !h.requireMedia(w) {
		return
	}
	var req struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.Room.SwitchSubtitle(req.Index); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"subtitle_index": req.Index})
}

// ---- Upload ----

// Upload handles file upload from host's browser.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<30) // 4GB max

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "file too large or invalid form")
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	// Create upload dir if needed
	if err := os.MkdirAll(h.UploadDir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create upload dir")
		return
	}

	// Save file — sanitize filename to prevent path traversal
	safeName := filepath.Base(header.Filename)
	if safeName == "." || safeName == ".." || safeName == "" || !h.isAllowedMediaExt(safeName) {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}
	dstPath := h.availableUploadPath(safeName)
	tmp, err := os.CreateTemp(h.UploadDir, ".syncwatch-upload-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save file")
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		writeError(w, http.StatusInternalServerError, "failed to write file")
		return
	}
	if err := tmp.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to finish upload")
		return
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to finish upload")
		return
	}

	h.Room.SetMedia(dstPath, room.SourceUpload)
	h.Room.Hub().Broadcast(signaling.Message{Type: signaling.MsgMedia, MediaURL: h.Room.GetMediaURL()})
	go h.probeCurrentMedia(dstPath)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "uploaded",
		"path":   dstPath,
		"url":    h.Room.GetMediaURL(),
	})
}

// UploadSubtitle handles subtitle file upload from host's browser.
func (h *Handler) UploadSubtitle(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10MB max for subtitles

	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "file too large or invalid form")
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	// Save subtitle file
	safeName := filepath.Base(header.Filename)
	if safeName == "." || safeName == ".." || safeName == "" || !isSubtitleExt(safeName) {
		writeError(w, http.StatusBadRequest, "unsupported subtitle format")
		return
	}
	if err := os.MkdirAll(h.UploadDir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create upload dir")
		return
	}
	dstPath := h.availableUploadPath(safeName)
	dst, err := os.Create(dstPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save file")
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write file")
		return
	}

	// Read and load the subtitle
	format, _, err := media.ReadSubtitleFile(dstPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read subtitle")
		return
	}

	// Add to room subtitles
	sub := media.SubtitleInfo{
		Path: dstPath, Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(dstPath)), "."),
	}
	if err := h.Room.AddSubtitle(sub); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load subtitle")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"format": format,
		"index":  h.Room.GetSubIndex(),
	})
}

// ServeFile serves local media files to viewers.
func (h *Handler) ServeFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path parameter required")
		return
	}

	cleanPath, err := h.allowedLocalFile(path)
	if err != nil {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	http.ServeFile(w, r, cleanPath)
}

// AllowedDirs returns dirs that are allowed for file serving.
func (h *Handler) AllowedDirs() []string {
	dirs := make([]string, len(h.ScanDirs))
	copy(dirs, h.ScanDirs)
	return dirs
}

// ---- Status / Info ----

// Status returns room and server status.
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	stats := h.Room.Stats()
	stats["audio_tracks"] = h.Room.GetAudioTracks()
	stats["subtitles"] = h.Room.GetSubtitles()
	stats["selected_audio"] = h.Room.GetAudioIndex()
	stats["selected_subtitle"] = h.Room.GetSubIndex()
	writeJSON(w, http.StatusOK, stats)
}

// MediaInfo returns ffprobe info for a media file.
func (h *Handler) MediaInfo(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter required")
		return
	}
	parsedURL, _ := url.Parse(path)
	if parsedURL == nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		var err error
		path, err = h.allowedLocalFile(path)
		if err != nil {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
	}
	info, err := media.Probe(h.FFprobePath, path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// MediaScan scans a directory for media files.
func (h *Handler) MediaScan(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	exts := h.AllowedExts
	if len(exts) == 0 {
		exts = []string{".mp4", ".mkv", ".avi", ".mov", ".webm"}
	}
	var scanDirs []string
	if dir == "" {
		scanDirs = h.ScanDirs
	} else if h.pathWithinAllowedDirs(dir, false) {
		scanDirs = []string{dir}
	} else {
		writeError(w, http.StatusForbidden, "directory is outside configured scan directories")
		return
	}
	var files []media.MediaInfo
	for _, scanDir := range scanDirs {
		found, err := media.ScanDir(scanDir, exts)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		files = append(files, found...)
	}
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].Path) < strings.ToLower(files[j].Path) })
	writeJSON(w, http.StatusOK, map[string]interface{}{"files": files})
}

// SignalingState returns the current room state.
func (h *Handler) SignalingState(w http.ResponseWriter, r *http.Request) {
	state := &signaling.RoomState{
		State:    h.Room.State().String(),
		Position: h.Room.GetPosition(),
		Speed:    h.Room.GetSpeed(),
	}
	if info := h.Room.GetMediaInfo(); info != nil {
		state.Media = &signaling.MediaState{
			Filename: info.Path,
			Duration: info.Duration,
		}
	}
	writeJSON(w, http.StatusOK, state)
}

// ---- Helpers ----

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (h *Handler) requireMedia(w http.ResponseWriter) bool {
	if h.Room.HasMedia() {
		return true
	}
	writeError(w, http.StatusConflict, "no media selected")
	return false
}

func (h *Handler) probeCurrentMedia(path string) {
	info, err := media.Probe(h.FFprobePath, path)
	if err == nil && h.Room.IsCurrentMedia(path) {
		var audioTracks []media.TrackInfo
		for _, track := range info.Tracks {
			if track.Type == "audio" {
				audioTracks = append(audioTracks, track)
			}
		}
		h.Room.SetMediaInfo(info, audioTracks)
	}
	if subs, _ := media.ExtractSubtitles("ffmpeg", path); len(subs) > 0 && h.Room.IsCurrentMedia(path) {
		h.Room.SetSubtitles(subs)
	}
}

func (h *Handler) allowedLocalFile(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil || !h.pathWithinAllowedDirs(absPath, true) {
		return "", os.ErrPermission
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", os.ErrNotExist
	}
	if !h.isAllowedMediaExt(absPath) {
		return "", os.ErrInvalid
	}
	return filepath.Clean(absPath), nil
}

func (h *Handler) pathWithinAllowedDirs(path string, includeUpload bool) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	dirs := h.AllowedDirs()
	if includeUpload {
		dirs = append(dirs, h.UploadDir)
	}
	for _, dir := range dirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absDir, absPath)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func (h *Handler) isAllowedMediaExt(path string) bool {
	exts := h.AllowedExts
	if len(exts) == 0 {
		exts = []string{".mp4", ".mkv", ".avi", ".mov", ".webm"}
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, allowed := range exts {
		if ext == strings.ToLower(allowed) {
			return true
		}
	}
	return false
}

func isSubtitleExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".srt", ".ass", ".ssa", ".vtt":
		return true
	default:
		return false
	}
}

func (h *Handler) availableUploadPath(name string) string {
	path := filepath.Join(h.UploadDir, name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		candidate := filepath.Join(h.UploadDir, fmt.Sprintf("%s-%d%s", base, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}
