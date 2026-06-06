package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kiseding/syncwatch/internal/media"
	"github.com/kiseding/syncwatch/internal/room"
	"github.com/kiseding/syncwatch/internal/signaling"
)

// Handler handles HTTP API requests.
type Handler struct {
	Room *room.Room
}

// NewHandler creates a new API handler.
func NewHandler(r *room.Room) *Handler {
	return &Handler{Room: r}
}

// Play starts or resumes playback.
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

	// Detect URL vs local path
	inputType := "local"
	if strings.HasPrefix(req.Path, "http://") || strings.HasPrefix(req.Path, "https://") {
		inputType = "url"
	}

	if err := h.Room.Play(r.Context(), req.Path, inputType); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "playing",
		"media":  h.Room.GetMediaInfo(),
	})
}

// Pause pauses playback.
func (h *Handler) Pause(w http.ResponseWriter, r *http.Request) {
	h.Room.Pause()
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// Resume resumes playback.
func (h *Handler) Resume(w http.ResponseWriter, r *http.Request) {
	if err := h.Room.Resume(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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

	if err := h.Room.Seek(r.Context(), req.Position); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

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

	// Validate speed
	validSpeeds := map[float64]bool{0.5: true, 1.0: true, 1.25: true, 1.5: true, 2.0: true}
	if !validSpeeds[req.Speed] {
		writeError(w, http.StatusBadRequest, "speed must be one of: 0.5, 1.0, 1.25, 1.5, 2.0")
		return
	}

	if err := h.Room.SetSpeed(r.Context(), req.Speed); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"speed": req.Speed,
	})
}

// SwitchAudio switches the audio track.
func (h *Handler) SwitchAudio(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.Room.SwitchAudioTrack(r.Context(), req.Index); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"audio_index": req.Index,
	})
}

// SwitchSubtitle switches the subtitle track.
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

	// Broadcast subtitle change to all viewers
	if subFmt, subContent, subIdx := h.Room.GetSubtitleData(); subIdx >= 0 {
		h.Room.Hub().Broadcast(signaling.Message{
			Type: signaling.MsgState,
			PlayState: &signaling.PlaybackState{
				Playing:  h.Room.State().String() == "playing",
				Position: h.Room.GetPosition(),
				Speed:    h.Room.GetSpeed(),
			},
		})
		// Also send subtitle via dedicated message
		h.Room.Hub().Broadcast(signaling.Message{
			Type: "subtitle",
			Text: subContent,
			From: subFmt,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"subtitle_index": req.Index,
	})
}

// Status returns room and server status.
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	stats := h.Room.Stats()

	// Add audio tracks info
	audioTracks := h.Room.GetAudioTracks()
	subtitles := h.Room.GetSubtitles()

	stats["audio_tracks"] = audioTracks
	stats["subtitles"] = subtitles
	stats["selected_audio"] = h.Room.GetAudioIndex()
	stats["selected_subtitle"] = h.Room.GetSubIndex()

	writeJSON(w, http.StatusOK, stats)
}

// MediaInfo returns information about media files.
func (h *Handler) MediaInfo(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter required")
		return
	}

	info, err := media.Probe("ffprobe", path)
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

	exts := []string{".mp4", ".mkv", ".avi", ".mov", ".webm"}
	files, err := media.ScanDir(dir, exts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"files": files,
	})
}

// SignalingState returns the current signaling/room state for a newly joined viewer.
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

// Helper functions
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
