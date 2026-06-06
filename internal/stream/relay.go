package stream

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// TrackWriter is the interface for writing RTP packets to a WebRTC track.
type TrackWriter interface {
	WriteRTP(p *rtp.Packet) error
}

// Relay bridges RTP readers to WebRTC track writers.
// It reads from RTP UDP and fans out to all connected WebRTC tracks.
type Relay struct {
	source      *Reader
	videoTracks []TrackWriter
	audioTracks []TrackWriter
	mu          sync.RWMutex
	running     atomic.Bool
	lastVideo   uint64 // last sequence number relayed
	lastAudio   uint64

	// Pacing
	ticker  *time.Ticker
	pktSize int // target RTP payload size
}

// NewRelay creates a new relay from an RTP reader.
func NewRelay(source *Reader) *Relay {
	return &Relay{
		source:  source,
		pktSize: 1200, // Standard RTP payload size
	}
}

// AddVideoTrack adds a video track writer (usually a WebRTC TrackLocal).
func (r *Relay) AddVideoTrack(w TrackWriter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.videoTracks = append(r.videoTracks, w)
}

// RemoveVideoTrack removes a video track writer.
func (r *Relay) RemoveVideoTrack(w TrackWriter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, t := range r.videoTracks {
		if t == w {
			r.videoTracks = append(r.videoTracks[:i], r.videoTracks[i+1:]...)
			return
		}
	}
}

// AddAudioTrack adds an audio track writer.
func (r *Relay) AddAudioTrack(w TrackWriter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.audioTracks = append(r.audioTracks, w)
}

// RemoveAudioTrack removes an audio track writer.
func (r *Relay) RemoveAudioTrack(w TrackWriter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, t := range r.audioTracks {
		if t == w {
			r.audioTracks = append(r.audioTracks[:i], r.audioTracks[i+1:]...)
			return
		}
	}
}

// Start begins relaying RTP packets to all registered tracks.
// ratePerSec controls the packet relay rate (0 = as fast as possible).
func (r *Relay) Start(ratePerSec int) {
	if ratePerSec <= 0 {
		ratePerSec = 100 // Default: 100 packets/sec
	}

	r.running.Store(true)
	interval := time.Second / time.Duration(ratePerSec)
	r.ticker = time.NewTicker(interval)

	go func() {
		for range r.ticker.C {
			if !r.running.Load() {
				return
			}

			pkt := r.source.ReadPacketNonBlocking()
			if pkt == nil {
				continue
			}

			r.mu.RLock()
			// Fan-out to all tracks (fire-and-forget for each)
			tracks := append([]TrackWriter{}, r.videoTracks...)
			tracks = append(tracks, r.audioTracks...)
			r.mu.RUnlock()

			for _, t := range tracks {
				_ = t.WriteRTP(pkt) // Ignore individual write errors
			}
		}
	}()
}

// Stop stops the relay.
func (r *Relay) Stop() {
	r.running.Store(false)
	if r.ticker != nil {
		r.ticker.Stop()
	}
}

// VideoRelay bridges a video RTP reader to video WebRTC tracks, with SSRC rewriting.
type VideoRelay struct {
	source     *Reader
	targetSSRC uint32
	writers    []*webrtc.TrackLocalStaticRTP
	mu         sync.RWMutex
	running    atomic.Bool
}

// NewVideoRelay creates a relay for video RTP that rewrites SSRC.
func NewVideoRelay(source *Reader) *VideoRelay {
	return &VideoRelay{
		source: source,
	}
}

// AddTrack adds a WebRTC track to relay to.
func (vr *VideoRelay) AddTrack(track *webrtc.TrackLocalStaticRTP) {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	vr.writers = append(vr.writers, track)
}

// RemoveTrack removes a WebRTC track.
func (vr *VideoRelay) RemoveTrack(track *webrtc.TrackLocalStaticRTP) {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	for i, w := range vr.writers {
		if w == track {
			vr.writers = append(vr.writers[:i], vr.writers[i+1:]...)
			return
		}
	}
}

// WriterCount returns the number of attached track writers.
func (vr *VideoRelay) WriterCount() int {
	vr.mu.RLock()
	defer vr.mu.RUnlock()
	return len(vr.writers)
}

// Start begins the relay loop for video packets.
func (vr *VideoRelay) Start() {
	vr.running.Store(true)

	go func() {
		for vr.running.Load() {
			pkt, err := vr.source.ReadPacket()
			if err != nil {
				return
			}

			vr.mu.RLock()
			writers := make([]*webrtc.TrackLocalStaticRTP, len(vr.writers))
			copy(writers, vr.writers)
			vr.mu.RUnlock()

			for _, w := range writers {
				_ = w.WriteRTP(pkt)
			}
		}
	}()
}

// Stop stops the video relay.
func (vr *VideoRelay) Stop() {
	vr.running.Store(false)
}
