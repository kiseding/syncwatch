package stream

import (
	"sync"
	"sync/atomic"

	"github.com/pion/webrtc/v4"
)

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
