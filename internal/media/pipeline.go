package media

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Status represents the current state of the FFmpeg pipeline.
type Status int32

const (
	StatusIdle     Status = iota
	StatusStarting        // FFmpeg process is starting
	StatusRunning         // FFmpeg is transcoding and RTP is flowing
	StatusPaused          // RTP output paused (process suspended)
	StatusStopping        // FFmpeg is being stopped
	StatusError           // FFmpeg encountered an error
)

func (s Status) String() string {
	switch s {
	case StatusIdle:
		return "idle"
	case StatusStarting:
		return "starting"
	case StatusRunning:
		return "running"
	case StatusPaused:
		return "paused"
	case StatusStopping:
		return "stopping"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// TrackInfo describes a media track from FFprobe.
type TrackInfo struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Type     string `json:"type"` // "video" | "audio" | "subtitle"
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Channels int    `json:"channels,omitempty"`
}

// MediaInfo holds metadata about a media file.
type MediaInfo struct {
	Path     string      `json:"path"`
	Duration float64     `json:"duration"`
	Format   string      `json:"format"`
	Size     int64       `json:"size"`
	Tracks   []TrackInfo `json:"tracks"`
}

// PipelineConfig configures a single FFmpeg transcode pipeline.
type PipelineConfig struct {
	InputPath    string  // Local file path or network URL
	InputType    string  // "local" or "url"
	SeekPosition float64 // Start position in seconds (-ss)
	Speed        float64 // Playback speed: 0.5, 1.0, 1.25, 1.5, 2.0
	VideoIndex   int     // Video stream index (-map 0:v:N)
	AudioIndex   int     // Audio stream index (-map 0:a:N)
	VideoPort    int     // RTP UDP output port for video
	AudioPort    int     // RTP UDP output port for audio
	VideoCodec   string  // e.g. "libvpx"
	AudioCodec   string  // e.g. "libopus"
	VideoBitrate string  // e.g. "2000k"
	AudioBitrate string  // e.g. "128k"
	FPS          int     // Output frame rate
	FFmpegPath   string  // Path to ffmpeg binary
}

// Pipeline manages the FFmpeg subprocess lifecycle.
type Pipeline struct {
	cfg    PipelineConfig
	cmd    *exec.Cmd
	status atomic.Int32
	mu     sync.Mutex
	cancel context.CancelFunc

	// Position tracking
	startTime  time.Time
	seekOffset float64 // seconds

	// Callbacks
	OnStatusChange func(Status)
	OnError        func(error)
}

// NewPipeline creates a new FFmpeg pipeline manager.
func NewPipeline(cfg PipelineConfig) *Pipeline {
	p := &Pipeline{
		cfg:        cfg,
		seekOffset: cfg.SeekPosition,
	}
	p.status.Store(int32(StatusIdle))
	return p
}

// buildArgs constructs the FFmpeg command line arguments.
func (p *Pipeline) buildArgs() []string {
	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-re", // Read input at native frame rate
	}

	// Seek position
	if p.cfg.SeekPosition > 0 {
		args = append(args, "-ss", strconv.FormatFloat(p.cfg.SeekPosition, 'f', 3, 64))
	}

	args = append(args,
		"-i", p.cfg.InputPath,
		"-fflags", "+genpts+igndts",
	)

	// === Video stream ===
	videoFilter := p.buildVideoFilter()
	args = append(args,
		"-map", fmt.Sprintf("0:v:%d", p.cfg.VideoIndex),
		"-c:v", p.cfg.VideoCodec,
	)

	// VP8 specific options
	if p.cfg.VideoCodec == "libvpx" {
		args = append(args,
			"-deadline", "realtime",
			"-cpu-used", "5",
			"-quality", "realtime",
			"-error-resilient", "1",
		)
	}

	if videoFilter != "" {
		args = append(args, "-vf", videoFilter)
	}

	args = append(args,
		"-b:v", p.cfg.VideoBitrate,
		"-maxrate", p.cfg.VideoBitrate,
		"-bufsize", p.cfg.VideoBitrate,
		"-g", "30",
		"-keyint_min", "30",
		"-r", strconv.Itoa(p.cfg.FPS),
		"-an", "-sn", "-dn",
		"-payload_type", "96",
		"-ssrc", "1001",
		"-max_delay", "0",
		"-f", "rtp",
		fmt.Sprintf("rtp://127.0.0.1:%d", p.cfg.VideoPort),
	)

	// === Audio stream ===
	audioFilter := p.buildAudioFilter()
	args = append(args,
		"-map", fmt.Sprintf("0:a:%d", p.cfg.AudioIndex),
		"-c:a", p.cfg.AudioCodec,
		"-b:a", p.cfg.AudioBitrate,
		"-application", "lowdelay",
		"-frame_duration", "20",
	)

	if audioFilter != "" {
		args = append(args, "-af", audioFilter)
	}

	args = append(args,
		"-vn", "-sn", "-dn",
		"-payload_type", "111",
		"-ssrc", "1002",
		"-f", "rtp",
		fmt.Sprintf("rtp://127.0.0.1:%d", p.cfg.AudioPort),
	)

	return args
}

// buildVideoFilter builds the video filter chain for scaling and speed.
func (p *Pipeline) buildVideoFilter() string {
	var filters []string

	// Speed control via setpts
	if p.cfg.Speed > 0 && p.cfg.Speed != 1.0 {
		ptsFactor := 1.0 / p.cfg.Speed
		filters = append(filters, fmt.Sprintf("setpts=%.4f*PTS", ptsFactor))
	}

	// Resolution cap at 1080p
	filters = append(filters,
		"scale='min(1920,iw)':'min(1080,ih)':force_original_aspect_ratio=decrease",
		"format=yuv420p",
	)

	return strings.Join(filters, ",")
}

// buildAudioFilter builds the audio filter chain for speed.
func (p *Pipeline) buildAudioFilter() string {
	if p.cfg.Speed <= 0 || p.cfg.Speed == 1.0 {
		return ""
	}
	// atempo supports 0.5-2.0 directly
	return fmt.Sprintf("atempo=%.3f", p.cfg.Speed)
}

// Start launches the FFmpeg subprocess.
func (p *Pipeline) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.Status() != StatusIdle && p.Status() != StatusError {
		return fmt.Errorf("pipeline already running (status: %s)", p.Status())
	}

	p.setStatus(StatusStarting)

	args := p.buildArgs()
	ctx, p.cancel = context.WithCancel(ctx)
	p.cmd = exec.CommandContext(ctx, p.cfg.FFmpegPath, args...)

	// Set process group so we can signal the entire group
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Capture stderr for error reporting
	stderr, err := p.cmd.StderrPipe()
	if err != nil {
		p.setStatus(StatusError)
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := p.cmd.Start(); err != nil {
		p.setStatus(StatusError)
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	p.startTime = time.Now()

	// Shared stderr buffer for error reporting
	var logBuf strings.Builder
	var logMu sync.Mutex

	// Monitor stderr in background
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				logMu.Lock()
				logBuf.Write(buf[:n])
				// Keep last 4KB for error reporting
				if logBuf.Len() > 4096 {
					logBuf.Reset()
					logBuf.Write(buf[:n])
				}
				logMu.Unlock()
			}
			if err != nil {
				break
			}
		}
	}()

	// Wait for process in background
	go func() {
		err := p.cmd.Wait()
		p.mu.Lock()
		defer p.mu.Unlock()
		if err != nil && p.Status() != StatusStopping && p.Status() != StatusIdle {
			p.setStatus(StatusError)
			if p.OnError != nil {
				logMu.Lock()
				errMsg := logBuf.String()
				logMu.Unlock()
				p.OnError(fmt.Errorf("ffmpeg exited: %w (stderr: %s)", err, errMsg))
			}
		}
	}()

	p.setStatus(StatusRunning)
	return nil
}

// Stop gracefully stops the FFmpeg process.
func (p *Pipeline) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.Status() == StatusIdle {
		return nil
	}

	p.setStatus(StatusStopping)

	if p.cancel != nil {
		p.cancel()
	}

	if p.cmd != nil && p.cmd.Process != nil {
		// Send SIGTERM to the process group
		if pgid, err := syscall.Getpgid(p.cmd.Process.Pid); err == nil {
			syscall.Kill(-pgid, syscall.SIGTERM)
		}

		// Give it 2 seconds to exit gracefully
		done := make(chan error, 1)
		go func() {
			done <- p.cmd.Wait()
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			// Force kill
			if pgid, err := syscall.Getpgid(p.cmd.Process.Pid); err == nil {
				syscall.Kill(-pgid, syscall.SIGKILL)
			}
			<-done
		}
	}

	p.setStatus(StatusIdle)
	return nil
}

// VideoAddr returns the UDP address FFmpeg is sending video RTP to.
func (p *Pipeline) VideoAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", p.cfg.VideoPort)
}

// AudioAddr returns the UDP address FFmpeg is sending audio RTP to.
func (p *Pipeline) AudioAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", p.cfg.AudioPort)
}

// Status returns the current pipeline status.
func (p *Pipeline) Status() Status {
	return Status(p.status.Load())
}

func (p *Pipeline) setStatus(s Status) {
	p.status.Store(int32(s))
	if p.OnStatusChange != nil {
		p.OnStatusChange(s)
	}
}

// Elapsed returns the current playback position in seconds.
func (p *Pipeline) Elapsed() float64 {
	switch p.Status() {
	case StatusRunning:
		return p.seekOffset + time.Since(p.startTime).Seconds()*p.cfg.Speed
	case StatusPaused:
		return p.seekOffset
	default:
		return p.seekOffset
	}
}

// UpdateSpeed adjusts playback speed. This requires restarting FFmpeg.
func (p *Pipeline) UpdateSpeed(ctx context.Context, speed float64) error {
	pos := p.Elapsed()
	if err := p.Stop(); err != nil {
		return err
	}
	p.cfg.Speed = speed
	p.cfg.SeekPosition = pos
	return p.Start(ctx)
}

// UpdateAudioTrack switches audio track. This requires restarting FFmpeg.
func (p *Pipeline) UpdateAudioTrack(ctx context.Context, index int) error {
	pos := p.Elapsed()
	if err := p.Stop(); err != nil {
		return err
	}
	p.cfg.AudioIndex = index
	p.cfg.SeekPosition = pos
	return p.Start(ctx)
}
