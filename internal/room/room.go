package room

import (
	"sync"
	"time"

	"github.com/kiseding/syncwatch/internal/media"
	"github.com/kiseding/syncwatch/internal/signaling"
)

// State represents the playback state of the room.
type State int32

const (
	StateIdle    State = iota
	StatePlaying
	StatePaused
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StatePlaying:
		return "playing"
	case StatePaused:
		return "paused"
	default:
		return "unknown"
	}
}

// SourceType describes where the media comes from.
type SourceType string

const (
	SourceURL    SourceType = "url"
	SourceLocal  SourceType = "local"
	SourceUpload SourceType = "upload"
)

// Room manages the single viewing room state.
// No media pipeline — viewers play the URL directly.
type Room struct {
	mu sync.RWMutex

	// Current media source
	mediaURL   string     // URL viewers should load (remote / local serve / upload serve)
	mediaInfo  *media.MediaInfo
	sourceType SourceType

	// Playback state
	state    State
	position float64
	speed    float64

	// Audio / Subtitle
	audioTracks []media.TrackInfo
	subs        []media.SubtitleInfo
	audioIndex  int
	subIndex    int
	subFormat   string
	subContent  string

	// Server base URL for constructing local/upload media URLs
	serverBaseURL string

	// WebSocket hub (kept for broadcasting state)
	hub *signaling.Hub

	// Upload storage dir
	uploadDir string

	// Stats
	createdAt  time.Time
	lastActive time.Time
}

// NewRoom creates a new room.
func NewRoom(hub *signaling.Hub, serverBaseURL, uploadDir string) *Room {
	return &Room{
		speed:         1.0,
		audioIndex:    0,
		subIndex:      -1,
		hub:           hub,
		serverBaseURL: serverBaseURL,
		uploadDir:     uploadDir,
		createdAt:     time.Now(),
		lastActive:    time.Now(),
	}
}

// ---- Setters (host only) ----

// SetMedia sets the current media source. For local/upload files,
// constructs a full HTTP URL that viewers can access.
func (r *Room) SetMedia(path string, sourceType SourceType) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.sourceType = sourceType
	r.position = 0
	r.state = StatePlaying
	r.lastActive = time.Now()

	switch sourceType {
	case SourceURL:
		r.mediaURL = path
	case SourceLocal, SourceUpload:
		r.mediaURL = r.serverBaseURL + "/api/media/file?path=" + path
	}

	r.broadcastState()
}

// SetMediaInfo stores ffprobe metadata and broadcasts updated track info.
func (r *Room) SetMediaInfo(info *media.MediaInfo, audioTracks []media.TrackInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mediaInfo = info
	r.audioTracks = audioTracks
	r.broadcastRoomInfo()
}

// SetSubtitles stores detected subtitles and broadcasts updated info.
func (r *Room) SetSubtitles(subs []media.SubtitleInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subs = subs
	if len(subs) > 0 {
		if fmt, cnt, err := media.ReadSubtitleFile(subs[0].Path); err == nil {
			r.subFormat, r.subContent, r.subIndex = fmt, cnt, 0
		}
	}
	r.broadcastRoomInfo()
}

// Pause pauses playback.
func (r *Room) Pause() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != StatePlaying {
		return
	}
	r.state = StatePaused
	r.broadcastState()
}

// Resume resumes playback.
func (r *Room) Resume() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != StatePaused {
		return
	}
	r.state = StatePlaying
	r.broadcastState()
}

// Seek sets the playback position.
func (r *Room) Seek(position float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.position = position
	r.state = StatePlaying
	r.lastActive = time.Now()

	r.hub.Broadcast(signaling.Message{
		Type: signaling.MsgSync,
		PlayState: &signaling.PlaybackState{
			Playing:  true,
			Position: position,
			Speed:    r.speed,
		},
	})
}

// SyncPosition updates the playback position without changing state.
// Used for periodic position sync from host to keep viewers in sync.
func (r *Room) SyncPosition(position float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.position = position

	r.hub.Broadcast(signaling.Message{
		Type: signaling.MsgSync,
		PlayState: &signaling.PlaybackState{
			Playing:  r.state == StatePlaying,
			Position: position,
			Speed:    r.speed,
		},
	})
}

// SetSpeed changes playback speed.
func (r *Room) SetSpeed(speed float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.speed = speed
	r.broadcastState()
}

// SwitchAudioTrack switches the audio track.
func (r *Room) SwitchAudioTrack(index int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.audioIndex = index
	r.broadcastState()
}

// SwitchSubtitle switches subtitle track.
func (r *Room) SwitchSubtitle(index int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if index < 0 || index >= len(r.subs) {
		r.subIndex = -1
		r.subFormat = ""
		r.subContent = ""
		r.broadcastState()
		return nil
	}

	format, content, err := media.ReadSubtitleFile(r.subs[index].Path)
	if err != nil {
		return err
	}
	r.subIndex = index
	r.subFormat = format
	r.subContent = content
	r.broadcastState()
	return nil
}

// ---- Getters ----

// GetMediaURL returns the URL viewers should load.
func (r *Room) GetMediaURL() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mediaURL
}

// GetMediaInfo returns media metadata.
func (r *Room) GetMediaInfo() *media.MediaInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mediaInfo
}

// State returns current playback state.
func (r *Room) State() State {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

// GetPosition returns current position.
func (r *Room) GetPosition() float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.position
}

// GetSpeed returns playback speed.
func (r *Room) GetSpeed() float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.speed
}

// GetAudioIndex returns selected audio track.
func (r *Room) GetAudioIndex() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.audioIndex
}

// GetSubIndex returns selected subtitle index.
func (r *Room) GetSubIndex() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.subIndex
}

// GetAudioTracks returns available audio tracks.
func (r *Room) GetAudioTracks() []media.TrackInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.audioTracks
}

// GetSubtitles returns available subtitles.
func (r *Room) GetSubtitles() []media.SubtitleInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.subs
}

// GetSubtitleData returns current subtitle data.
func (r *Room) GetSubtitleData() (format, content string, index int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.subFormat, r.subContent, r.subIndex
}

// GetSourceType returns the media source type.
func (r *Room) GetSourceType() SourceType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sourceType
}

// Hub returns the signaling hub.
func (r *Room) Hub() *signaling.Hub {
	return r.hub
}

// Stats returns room statistics.
func (r *Room) Stats() map[string]interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	dur := 0.0
	if r.mediaInfo != nil {
		dur = r.mediaInfo.Duration
	}
	return map[string]interface{}{
		"state":       r.state.String(),
		"position":    r.position,
		"duration":    dur,
		"speed":       r.speed,
		"media_url":   r.mediaURL,
		"source_type": string(r.sourceType),
		"created_at":  r.createdAt.Format(time.RFC3339),
		"last_active": r.lastActive.Format(time.RFC3339),
	}
}

// broadcastState sends current playback state to all viewers.
func (r *Room) broadcastState() {
	r.hub.Broadcast(signaling.Message{
		Type: signaling.MsgState,
		PlayState: &signaling.PlaybackState{
			Playing:  r.state == StatePlaying,
			Position: r.position,
			Speed:    r.speed,
		},
	})
}

// broadcastRoomInfo sends updated audio/subtitle track info to all clients.
func (r *Room) broadcastRoomInfo() {
	var audioTracks []signaling.TrackInfo
	for _, t := range r.audioTracks {
		audioTracks = append(audioTracks, signaling.TrackInfo{
			Index: t.Index, Type: "audio",
			Language: t.Language, Title: t.Title,
		})
	}
	var subTracks []signaling.TrackInfo
	for _, s := range r.subs {
		subTracks = append(subTracks, signaling.TrackInfo{
			Index: s.Index, Type: "subtitle",
			Language: s.Language, Title: s.Path,
		})
	}
	dur := 0.0
	if r.mediaInfo != nil {
		dur = r.mediaInfo.Duration
	}
	r.hub.Broadcast(signaling.Message{
		Type: signaling.MsgJoined,
		RoomState: &signaling.RoomState{
			Media:         &signaling.MediaState{Filename: r.mediaURL, Duration: dur},
			AudioTracks:   audioTracks,
			SubTracks:     subTracks,
			SelectedAudio: r.audioIndex,
			SelectedSub:   r.subIndex,
		},
	})
}
