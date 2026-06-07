package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	if strings.HasPrefix(req.Path, "http://") || strings.HasPrefix(req.Path, "https://") {
		sourceType = room.SourceURL
	} else {
		sourceType = room.SourceLocal
	}

	// Probe media in background
	go func() {
		info, err := media.Probe(h.FFprobePath, req.Path)
		if err == nil {
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
		if len(subs) > 0 {
			h.Room.SetSubtitles(subs)
		}
	}()

	h.Room.SetMedia(req.Path, sourceType)

	// Broadcast media change to all viewers
	url := h.Room.GetMediaURL()
	h.Room.Hub().Broadcast(signaling.Message{
		Type: signaling.MsgState,
		PlayState: &signaling.PlaybackState{
			Playing:  true,
			Position: 0,
			Speed:    h.Room.GetSpeed(),
		},
		Text: url,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "playing",
		"url":    url,
	})
}

// Pause pauses playback.
func (h *Handler) Pause(w http.ResponseWriter, r *http.Request) {
	h.Room.Pause()
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// Resume resumes playback.
func (h *Handler) Resume(w http.ResponseWriter, r *http.Request) {
	h.Room.Resume()
	writeJSON(w, http.StatusOK, map[string]string{"status": "playing"})
}

// Seek seeks to a position.
func (h *Handler) Seek(w http.ResponseWriter, r *http.Request) {
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
		"position": req.Position,
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

// SwitchAudio switches audio track.
func (h *Handler) SwitchAudio(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	h.Room.SwitchAudioTrack(req.Index)
	writeJSON(w, http.StatusOK, map[string]interface{}{"audio_index": req.Index})
}

// SwitchSubtitle switches subtitle track.
func (h *Handler) SwitchSubtitle(w http.ResponseWriter, r *http.Request) {
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

	// Broadcast subtitle data to viewers
	if subFmt, subContent, subIdx := h.Room.GetSubtitleData(); subIdx >= 0 {
		h.Room.Hub().Broadcast(signaling.Message{
			Type: "subtitle",
			Text: subContent,
			From: subFmt,
		})
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

	// Save file
	dstPath := filepath.Join(h.UploadDir, header.Filename)
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

	h.Room.SetMedia(dstPath, room.SourceUpload)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "uploaded",
		"path":   dstPath,
		"url":    h.Room.GetMediaURL(),
	})
}

// ServeFile serves local media files to viewers.
func (h *Handler) ServeFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path parameter required")
		return
	}

	// Security: only allow files within scan dirs or upload dir
	allowed := false
	absPath, _ := filepath.Abs(path)
	for _, dir := range h.AllowedDirs() {
		absDir, _ := filepath.Abs(dir)
		if strings.HasPrefix(absPath, absDir) {
			allowed = true
			break
		}
	}
	if absPath == h.UploadDir || strings.HasPrefix(absPath, h.UploadDir+string(filepath.Separator)) {
		allowed = true
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	http.ServeFile(w, r, path)
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
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		allowed := false
		absPath, _ := filepath.Abs(path)
		for _, dir := range h.AllowedDirs() {
			absDir, _ := filepath.Abs(dir)
			if strings.HasPrefix(absPath, absDir) {
				allowed = true
				break
			}
		}
		if absPath == h.UploadDir || strings.HasPrefix(absPath, h.UploadDir+string(filepath.Separator)) {
			allowed = true
		}
		if !allowed {
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
	if dir == "" {
		writeError(w, http.StatusBadRequest, "dir query parameter required")
		return
	}
	exts := h.AllowedExts
	if len(exts) == 0 {
		exts = []string{".mp4", ".mkv", ".avi", ".mov", ".webm"}
	}
	files, err := media.ScanDir(dir, exts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
