package room

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kiseding/syncwatch/internal/media"
	"github.com/kiseding/syncwatch/internal/signaling"
	"github.com/kiseding/syncwatch/internal/stream"
	"github.com/kiseding/syncwatch/internal/webrtc"
)

// State represents the playback state of the room.
type State int32

const (
	StateIdle    State = iota
	StateLoading       // Media is being loaded / FFmpeg starting
	StatePlaying       // Video is playing and streaming
	StatePaused        // Playback paused
	StateSeeking       // Seeking to a new position
	StateError         // Error state
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateLoading:
		return "loading"
	case StatePlaying:
		return "playing"
	case StatePaused:
		return "paused"
	case StateSeeking:
		return "seeking"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// Room manages the single viewing room state.
type Room struct {
	// Playback state
	state       atomic.Int32
	position    float64 // current playback position in seconds
	speed       float64 // current playback speed
	mu          sync.RWMutex

	// Current media
	mediaInfo   *media.MediaInfo
	audioTracks []media.TrackInfo
	subs        []media.SubtitleInfo
	audioIndex  int    // selected audio track index
	subIndex    int    // selected subtitle index (-1 = none)
	subFormat   string // loaded subtitle format
	subContent  string // loaded subtitle content
	inputType   string // "local" or "url" — preserved for seek restarts

	// Pipeline
	pipeline *media.Pipeline
	videoRTP *stream.Reader
	audioRTP *stream.Reader
	videoRelay *stream.VideoRelay
	audioRelay *stream.VideoRelay

	// WebRTC
	sfu        *webrtc.SFU
	hub        *signaling.Hub
	hostID     string

	// Configuration
	ffmpegPath  string
	ffprobePath string
	videoPort   int
	audioPort   int
	videoCodec  string
	audioCodec  string
	videoBitrate string
	audioBitrate string
	fps         int

	// Stats
	createdAt  time.Time
	lastActive time.Time
}

// NewRoom creates a new room with the given SFU and Hub.
func NewRoom(sfu *webrtc.SFU, hub *signaling.Hub, ffmpegPath, ffprobePath string) *Room {
	r := &Room{
		speed:        1.0,
		audioIndex:   0,
		subIndex:     -1,
		sfu:          sfu,
		hub:          hub,
		ffmpegPath:   ffmpegPath,
		ffprobePath:  ffprobePath,
		videoPort:    5004,
		audioPort:    5006,
		videoCodec:   "libvpx",
		audioCodec:   "libopus",
		videoBitrate: "2000k",
		audioBitrate: "128k",
		fps:          30,
		createdAt:    time.Now(),
		lastActive:   time.Now(),
	}
	r.state.Store(int32(StateIdle))
	return r
}

// SetTranscodeConfig updates the transcode settings.
func (r *Room) SetTranscodeConfig(videoCodec, audioCodec, videoBitrate, audioBitrate string, fps int) {
	r.videoCodec = videoCodec
	r.audioCodec = audioCodec
	r.videoBitrate = videoBitrate
	r.audioBitrate = audioBitrate
	r.fps = fps
}

// SetPorts updates the RTP ports.
func (r *Room) SetPorts(videoPort, audioPort int) {
	r.videoPort = videoPort
	r.audioPort = audioPort
}

// SetHost sets the host client ID.
func (r *Room) SetHost(id string) {
	r.hostID = id
}

// HostID returns the host client ID.
func (r *Room) HostID() string {
	return r.hostID
}

// State returns the current playback state.
func (r *Room) State() State {
	return State(r.state.Load())
}

// Play loads and starts playing a media file or URL.
// The lock is only held during fast state mutations; slow I/O (ffprobe, ffmpeg start)
// runs outside the lock so frontend state polling doesn't block.
func (r *Room) Play(ctx context.Context, filePath, inputType string) error {
	// Phase 1: Stop old pipeline (under lock)
	r.mu.Lock()
	r.inputType = inputType

	if r.pipeline != nil {
		r.pipeline.Stop()
	}
	if r.videoRTP != nil {
		r.videoRTP.Stop()
	}
	if r.audioRTP != nil {
		r.audioRTP.Stop()
	}
	r.pipeline = nil
	r.videoRTP = nil
	r.audioRTP = nil
	r.videoRelay = nil
	r.audioRelay = nil

	r.state.Store(int32(StateLoading))
	r.mu.Unlock()

	// Phase 2: Probe media (no lock — slow network I/O)
	info, err := media.Probe(r.ffprobePath, filePath)
	if err != nil {
		r.state.Store(int32(StateError))
		return fmt.Errorf("probe media: %w", err)
	}

	// Analyze tracks (no lock needed — local computation)
	videoIdx := -1
	audioIdx := -1
	var audioTracks []media.TrackInfo
	for _, t := range info.Tracks {
		if t.Type == "video" && videoIdx < 0 {
			videoIdx = t.Index
		}
		if t.Type == "audio" {
			if audioIdx < 0 {
				audioIdx = t.Index
			}
			audioTracks = append(audioTracks, t)
		}
	}
	if videoIdx < 0 {
		r.state.Store(int32(StateError))
		return fmt.Errorf("no video track found")
	}
	if audioIdx < 0 {
		audioIdx = 0
	}

	// External subtitles (no lock — filesystem I/O)
	subs, _ := media.ExtractSubtitles(r.ffmpegPath, filePath)
	var subFormat, subContent string
	subIndex := -1
	if len(subs) > 0 {
		if fmt, cnt, err := media.ReadSubtitleFile(subs[0].Path); err == nil {
			subFormat, subContent, subIndex = fmt, cnt, 0
		}
	}

	// RTP readers (no lock — UDP bind only)
	videoRTP, err := stream.NewReader(r.videoPort, 200)
	if err != nil {
		r.state.Store(int32(StateError))
		return fmt.Errorf("create video RTP reader: %w", err)
	}
	audioRTP, err := stream.NewReader(r.audioPort, 200)
	if err != nil {
		videoRTP.Stop()
		r.state.Store(int32(StateError))
		return fmt.Errorf("create audio RTP reader: %w", err)
	}

	// Build pipeline config (read-only config fields, no lock needed)
	r.mu.RLock()
	cfg := media.PipelineConfig{
		InputPath:    filePath,
		InputType:    inputType,
		SeekPosition: 0,
		Speed:        r.speed,
		VideoIndex:   videoIdx,
		AudioIndex:   r.audioIndex,
		VideoPort:    r.videoPort,
		AudioPort:    r.audioPort,
		VideoCodec:   r.videoCodec,
		AudioCodec:   r.audioCodec,
		VideoBitrate: r.videoBitrate,
		AudioBitrate: r.audioBitrate,
		FPS:          r.fps,
		FFmpegPath:   r.ffmpegPath,
	}
	r.mu.RUnlock()

	pipeline := media.NewPipeline(cfg)
	pipeline.OnStatusChange = func(s media.Status) {
		if s == media.StatusError {
			r.state.Store(int32(StateError))
		}
	}
	pipeline.OnError = func(err error) {
		r.state.Store(int32(StateError))
		fmt.Printf("[room] pipeline error: %v\n", err)
	}

	// Phase 3: Start pipeline (may be slow for remote URLs, no lock)
	if err := pipeline.Start(context.Background()); err != nil {
		videoRTP.Stop()
		audioRTP.Stop()
		r.state.Store(int32(StateError))
		return fmt.Errorf("start pipeline: %w", err)
	}
	if pipeline.Status() != media.StatusRunning {
		videoRTP.Stop()
		audioRTP.Stop()
		r.state.Store(int32(StateError))
		return fmt.Errorf("pipeline failed to start")
	}

	// Phase 4: Commit everything under lock
	r.mu.Lock()
	r.mediaInfo = info
	r.audioTracks = audioTracks
	r.subs = subs
	r.subFormat = subFormat
	r.subContent = subContent
	r.subIndex = subIndex
	r.videoRTP = videoRTP
	r.audioRTP = audioRTP
	r.videoRelay = stream.NewVideoRelay(videoRTP)
	r.audioRelay = stream.NewVideoRelay(audioRTP)
	r.pipeline = pipeline
	r.position = 0
	r.lastActive = time.Now()

	// Attach all existing viewer tracks
	for _, v := range r.sfu.GetAllViewers() {
		r.videoRelay.AddTrack(v.VideoTrack)
		r.audioRelay.AddTrack(v.AudioTrack)
	}
	r.mu.Unlock()

	// Phase 5: Start RTP readers and relays (no lock needed)
	videoRTP.Start()
	audioRTP.Start()
	r.videoRelay.Start()
	r.audioRelay.Start()

	r.state.Store(int32(StatePlaying))

	// Broadcast state change
	r.hub.Broadcast(signaling.Message{
		Type: signaling.MsgState,
		PlayState: &signaling.PlaybackState{
			Playing:  true,
			Position: 0,
			Speed:    cfg.Speed,
		},
	})

	return nil
}

// Pause pauses playback.
func (r *Room) Pause() {
	if r.State() != StatePlaying {
		return
	}

	r.mu.Lock()
	r.position = r.pipeline.Elapsed()
	r.mu.Unlock()

	r.state.Store(int32(StatePaused))
	r.videoRelay.Stop()
	r.audioRelay.Stop()

	r.hub.Broadcast(signaling.Message{
		Type: signaling.MsgState,
		PlayState: &signaling.PlaybackState{
			Playing:  false,
			Position: r.position,
			Speed:    r.speed,
		},
	})
}

// Resume resumes playback.
func (r *Room) Resume(ctx context.Context) error {
	if r.State() != StatePaused {
		return nil
	}

	r.videoRelay.Start()
	r.audioRelay.Start()

	r.state.Store(int32(StatePlaying))

	r.hub.Broadcast(signaling.Message{
		Type: signaling.MsgState,
		PlayState: &signaling.PlaybackState{
			Playing:  true,
			Position: r.position,
			Speed:    r.speed,
		},
	})

	return nil
}

// Seek seeks to the given position (seconds).
func (r *Room) Seek(ctx context.Context, position float64) error {
	// Phase 1: Stop old pipeline and clear buffers (under lock)
	r.mu.Lock()
	r.state.Store(int32(StateSeeking))

	r.hub.Broadcast(signaling.Message{
		Type: signaling.MsgSync,
		PlayState: &signaling.PlaybackState{
			Playing:  false,
			Position: position,
			Speed:    r.speed,
		},
	})

	if r.pipeline != nil {
		r.pipeline.Stop()
		r.pipeline = nil
	}
	if r.videoRTP != nil {
		r.videoRTP.ClearBuffer()
	}
	if r.audioRTP != nil {
		r.audioRTP.ClearBuffer()
	}
	r.position = position

	// Snapshot config for pipeline (under lock)
	inputType := r.inputType
	if inputType == "" {
		inputType = "local"
	}
	mediaPath := ""
	if r.mediaInfo != nil {
		mediaPath = r.mediaInfo.Path
	}
	cfg := media.PipelineConfig{
		InputPath:    mediaPath,
		InputType:    inputType,
		SeekPosition: position,
		Speed:        r.speed,
		VideoIndex:   r.pipelineCfgVideoIdx(),
		AudioIndex:   r.audioIndex,
		VideoPort:    r.videoPort,
		AudioPort:    r.audioPort,
		VideoCodec:   r.videoCodec,
		AudioCodec:   r.audioCodec,
		VideoBitrate: r.videoBitrate,
		AudioBitrate: r.audioBitrate,
		FPS:          r.fps,
		FFmpegPath:   r.ffmpegPath,
	}
	r.mu.Unlock()

	// Phase 2: Create and start pipeline (no lock — may be slow)
	pipeline := media.NewPipeline(cfg)
	pipeline.OnStatusChange = func(s media.Status) {
		if s == media.StatusError {
			r.state.Store(int32(StateError))
		}
	}
	pipeline.OnError = func(err error) {
		r.state.Store(int32(StateError))
		fmt.Printf("[room] pipeline error (seek): %v\n", err)
	}

	if err := pipeline.Start(context.Background()); err != nil {
		r.state.Store(int32(StateError))
		return fmt.Errorf("restart pipeline for seek: %w", err)
	}
	if pipeline.Status() != media.StatusRunning {
		r.state.Store(int32(StateError))
		return fmt.Errorf("pipeline failed after seek")
	}

	// Phase 3: Commit (under lock)
	r.mu.Lock()
	r.pipeline = pipeline
	r.mu.Unlock()

	r.state.Store(int32(StatePlaying))

	r.hub.Broadcast(signaling.Message{
		Type: signaling.MsgState,
		PlayState: &signaling.PlaybackState{
			Playing:  true,
			Position: position,
			Speed:    r.speed,
		},
	})

	return nil
}

// SetSpeed changes playback speed.
func (r *Room) SetSpeed(ctx context.Context, speed float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.speed = speed

	if r.pipeline != nil && r.State() == StatePlaying {
		pos := r.pipeline.Elapsed()
		if err := r.pipeline.UpdateSpeed(context.Background(), speed); err != nil {
			return err
		}
		r.position = pos
	}

	r.hub.Broadcast(signaling.Message{
		Type: signaling.MsgState,
		PlayState: &signaling.PlaybackState{
			Playing:  r.State() == StatePlaying,
			Position: r.position,
			Speed:    r.speed,
		},
	})

	return nil
}

// SwitchAudioTrack switches to the given audio track index.
func (r *Room) SwitchAudioTrack(ctx context.Context, index int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.audioIndex = index

	if r.pipeline != nil && r.State() == StatePlaying {
		if err := r.pipeline.UpdateAudioTrack(context.Background(), index); err != nil {
			return err
		}
	}

	return nil
}

// GetMediaInfo returns the current media information.
func (r *Room) GetMediaInfo() *media.MediaInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mediaInfo
}

// GetAudioTracks returns available audio tracks.
func (r *Room) GetAudioTracks() []media.TrackInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.audioTracks
}

// GetSubtitles returns available subtitle files.
func (r *Room) GetSubtitles() []media.SubtitleInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.subs
}

// GetSubtitleData returns the currently loaded subtitle format and content.
func (r *Room) GetSubtitleData() (format, content string, index int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.subFormat, r.subContent, r.subIndex
}

// SwitchSubtitle switches to a different subtitle track or disables (-1).
func (r *Room) SwitchSubtitle(index int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if index < 0 || index >= len(r.subs) {
		r.subIndex = -1
		r.subFormat = ""
		r.subContent = ""
		return nil
	}

	format, content, err := media.ReadSubtitleFile(r.subs[index].Path)
	if err != nil {
		return err
	}

	r.subIndex = index
	r.subFormat = format
	r.subContent = content
	return nil
}

// GetPosition returns the current playback position.
func (r *Room) GetPosition() float64 {
	if r.pipeline != nil {
		return r.pipeline.Elapsed()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.position
}

// GetDuration returns the media duration.
func (r *Room) GetDuration() float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.mediaInfo != nil {
		return r.mediaInfo.Duration
	}
	return 0
}

// GetSpeed returns the current playback speed.
func (r *Room) GetSpeed() float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.speed
}

// GetAudioIndex returns the selected audio track index.
func (r *Room) GetAudioIndex() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.audioIndex
}

// GetSubIndex returns the selected subtitle index.
func (r *Room) GetSubIndex() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.subIndex
}

// Stats returns room statistics.
func (r *Room) Stats() map[string]interface{} {
	return map[string]interface{}{
		"state":       r.State().String(),
		"position":    r.GetPosition(),
		"duration":    r.GetDuration(),
		"speed":       r.speed,
		"viewers":     r.sfu.ViewerCount(),
		"created_at":  r.createdAt.Format(time.RFC3339),
		"last_active": r.lastActive.Format(time.RFC3339),
	}
}

// pipelineCfgVideoIdx returns the video index for pipeline config.
func (r *Room) pipelineCfgVideoIdx() int {
	if r.mediaInfo == nil {
		return 0
	}
	for _, t := range r.mediaInfo.Tracks {
		if t.Type == "video" {
			return t.Index
		}
	}
	return 0
}

// SFU returns the SFU instance.
func (r *Room) SFU() *webrtc.SFU {
	return r.sfu
}

// Hub returns the signaling hub.
func (r *Room) Hub() *signaling.Hub {
	return r.hub
}

// AddViewer attaches a viewer's tracks to the relays.
func (r *Room) AddViewer(session *webrtc.ViewerSession) {
	if r.videoRelay != nil {
		r.videoRelay.AddTrack(session.VideoTrack)
	}
	if r.audioRelay != nil {
		r.audioRelay.AddTrack(session.AudioTrack)
	}
}

// RemoveViewer removes a viewer's tracks from the relays.
func (r *Room) RemoveViewer(session *webrtc.ViewerSession) {
	if r.videoRelay != nil {
		r.videoRelay.RemoveTrack(session.VideoTrack)
	}
	if r.audioRelay != nil {
		r.audioRelay.RemoveTrack(session.AudioTrack)
	}
}
