package webrtc

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

// ViewerSession represents one connected viewer's WebRTC state.
type ViewerSession struct {
	ID          string
	PeerConn    *pion.PeerConnection
	VideoTrack  *pion.TrackLocalStaticRTP
	AudioTrack  *pion.TrackLocalStaticRTP
	SyncChannel *pion.DataChannel
	Role        string // "host" or "viewer"

	OnSyncMessage func(msg []byte) // Called when DataChannel message received
	OnChatMessage func(from string, msg []byte)

	mu sync.RWMutex
}

// ICEStatus wraps ICE connection state information.
type ICEStatus struct {
	State     pion.ICEConnectionState
	ViewerID  string
	Role      string
}

// SFU manages WebRTC sessions for all viewers.
type SFU struct {
	api         *pion.API
	iceServers  []pion.ICEServer
	viewers     map[string]*ViewerSession
	mu          sync.RWMutex
	nextID      uint64

	// Callbacks
	OnViewerJoin   func(viewer *ViewerSession)
	OnViewerLeave  func(viewerID string)
	OnICEStatus    func(status ICEStatus)
}

// NewSFU creates a new Selective Forwarding Unit manager.
func NewSFU(iceServers []pion.ICEServer) *SFU {
	// Setting engine for proper ICE candidate discovery
	s := pion.SettingEngine{}
	s.SetIncludeLoopbackCandidate(true)
	s.SetICETimeouts(5*time.Second, 25*time.Second, 2*time.Second)

	// Media engine with default codecs
	m := &pion.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		panic(fmt.Sprintf("register codecs: %v", err))
	}

	api := pion.NewAPI(
		pion.WithSettingEngine(s),
		pion.WithMediaEngine(m),
	)

	return &SFU{
		api:        api,
		iceServers: iceServers,
		viewers:    make(map[string]*ViewerSession),
	}
}

// CreateSession creates a new viewer session with pre-configured tracks.
func (s *SFU) CreateSession(viewerID, role string) (*ViewerSession, error) {
	config := pion.Configuration{
		ICEServers: s.iceServers,
	}

	pc, err := s.api.NewPeerConnection(config)
	if err != nil {
		return nil, fmt.Errorf("create peer connection: %w", err)
	}

	// Create video and audio tracks for this viewer
	videoTrack, err := pion.NewTrackLocalStaticRTP(
		pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8},
		"video", "syncwatch",
	)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("create video track: %w", err)
	}

	audioTrack, err := pion.NewTrackLocalStaticRTP(
		pion.RTPCodecCapability{MimeType: pion.MimeTypeOpus},
		"audio", "syncwatch",
	)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("create audio track: %w", err)
	}

	// Add tracks to peer connection
	if _, err := pc.AddTrack(videoTrack); err != nil {
		pc.Close()
		return nil, fmt.Errorf("add video track: %w", err)
	}
	if _, err := pc.AddTrack(audioTrack); err != nil {
		pc.Close()
		return nil, fmt.Errorf("add audio track: %w", err)
	}

	// Create DataChannel for sync (all roles need it)
	syncChannel, err := pc.CreateDataChannel("sync", &pion.DataChannelInit{
		Ordered: func() *bool { v := true; return &v }(),
	})
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("create sync channel: %w", err)
	}

	session := &ViewerSession{
		ID:          viewerID,
		PeerConn:    pc,
		VideoTrack:  videoTrack,
		AudioTrack:  audioTrack,
		SyncChannel: syncChannel,
		Role:        role,
	}

	// Handle incoming DataChannel (from viewer)
	pc.OnDataChannel(func(dc *pion.DataChannel) {
		dc.OnMessage(func(msg pion.DataChannelMessage) {
			switch dc.Label() {
			case "sync":
				if session.OnSyncMessage != nil {
					session.OnSyncMessage(msg.Data)
				}
			case "chat":
				if session.OnChatMessage != nil {
					session.OnChatMessage(session.ID, msg.Data)
				}
			}
		})
	})

	// Monitor ICE connection state
	pc.OnICEConnectionStateChange(func(state pion.ICEConnectionState) {
		if s.OnICEStatus != nil {
			s.OnICEStatus(ICEStatus{State: state, ViewerID: viewerID, Role: role})
		}
	})

	// Auto-cleanup on disconnection
	pc.OnConnectionStateChange(func(state pion.PeerConnectionState) {
		if state == pion.PeerConnectionStateDisconnected ||
			state == pion.PeerConnectionStateFailed ||
			state == pion.PeerConnectionStateClosed {
			s.RemoveSession(viewerID)
		}
	})

	s.mu.Lock()
	s.viewers[viewerID] = session
	s.mu.Unlock()

	if s.OnViewerJoin != nil {
		s.OnViewerJoin(session)
	}

	return session, nil
}

// RemoveSession removes and cleans up a viewer session.
func (s *SFU) RemoveSession(viewerID string) {
	s.mu.Lock()
	session, exists := s.viewers[viewerID]
	if !exists {
		s.mu.Unlock()
		return
	}
	delete(s.viewers, viewerID)
	s.mu.Unlock()

	if session.PeerConn != nil {
		session.PeerConn.Close()
	}

	if s.OnViewerLeave != nil {
		s.OnViewerLeave(viewerID)
	}
}

// GetSession returns a viewer session by ID.
func (s *SFU) GetSession(id string) *ViewerSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.viewers[id]
}

// ViewerCount returns the current number of connected viewers.
func (s *SFU) ViewerCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.viewers)
}

// GenerateViewerID generates a unique viewer ID.
func (s *SFU) GenerateViewerID() string {
	id := atomic.AddUint64(&s.nextID, 1)
	return fmt.Sprintf("viewer-%d", id)
}

// BroadcastSync sends a message to all viewers via DataChannel.
func (s *SFU) BroadcastSync(msg []byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, v := range s.viewers {
		if v.SyncChannel != nil && v.SyncChannel.ReadyState() == pion.DataChannelStateOpen {
			v.SyncChannel.Send(msg)
		}
	}
}

// BroadcastRTP fowards an RTP packet to all viewer tracks.
func (s *SFU) BroadcastRTP(pkt *rtp.Packet, trackKind string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, v := range s.viewers {
		var err error
		if trackKind == "video" && v.VideoTrack != nil {
			err = v.VideoTrack.WriteRTP(pkt)
		} else if trackKind == "audio" && v.AudioTrack != nil {
			err = v.VideoTrack.WriteRTP(pkt)
		}
		_ = err // Individual write failures are non-fatal
	}
}

// GetAllViewers returns a copy of the viewer list.
func (s *SFU) GetAllViewers() []*ViewerSession {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*ViewerSession, 0, len(s.viewers))
	for _, v := range s.viewers {
		result = append(result, v)
	}
	return result
}

// GetHostSession returns the host session, or nil if no host connected.
func (s *SFU) GetHostSession() *ViewerSession {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, v := range s.viewers {
		if v.Role == "host" {
			return v
		}
	}
	return nil
}
