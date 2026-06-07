package signaling

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Message types for WebSocket communication.
const (
	MsgJoin   = "join"
	MsgJoined = "joined"
	MsgMedia  = "media"  // new media loaded, viewer should change video.src
	MsgState  = "state"
	MsgSync   = "sync"
	MsgSystem = "system"
	MsgError  = "error"
)

// Message represents a generic WebSocket message.
type Message struct {
	Type      string          `json:"type"`
	Role      string          `json:"role,omitempty"`
	RoomState *RoomState      `json:"room_state,omitempty"`
	PlayState *PlaybackState  `json:"play_state,omitempty"`
	Text      string          `json:"text,omitempty"`
	From      string          `json:"from,omitempty"`
	System    bool            `json:"system,omitempty"`
	Message   string          `json:"message,omitempty"`
	Timestamp int64           `json:"timestamp,omitempty"`
	MediaURL  string          `json:"media_url,omitempty"`
	Raw       json.RawMessage `json:"-"`
}

// RoomState sent to newly joined viewers.
type RoomState struct {
	State         string            `json:"state"`
	Position      float64           `json:"position"`
	Speed         float64           `json:"speed"`
	Media         *MediaState       `json:"media,omitempty"`
	Subtitle      *SubtitleData     `json:"subtitle,omitempty"`
	AudioTracks   []TrackInfo       `json:"audio_tracks,omitempty"`
	SubTracks     []TrackInfo       `json:"subtitle_tracks,omitempty"`
	SelectedAudio int `json:"selected_audio"`
	SelectedSub   int `json:"selected_sub"`
}

// MediaState describes the currently loaded media.
type MediaState struct {
	Filename string  `json:"filename"`
	Duration float64 `json:"duration"`
}

// SubtitleData carries subtitle content to viewers.
type SubtitleData struct {
	Format  string `json:"format"`  // "ass", "srt", "ssa", "vtt"
	Content string `json:"content"` // subtitle file content
	Index   int    `json:"index"`   // track index
}

// TrackInfo describes a media track for signaling.
type TrackInfo struct {
	Index    int    `json:"index"`
	Type     string `json:"type"` // "audio" or "subtitle"
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
}

// PlaybackState broadcast to all viewers on state change.
type PlaybackState struct {
	Playing  bool    `json:"playing"`
	Position float64 `json:"position"`
	Speed    float64 `json:"speed"`
}

// Client represents a WebSocket-connected client.
type Client struct {
	ID        string
	Role      string // "host" or "viewer"
	Conn      *websocket.Conn
	Send      chan []byte
	hub       *Hub
	mu        sync.Mutex
	OnMessage func(client *Client, msg Message) // per-client message handler
}

// Hub manages all WebSocket connections for signaling.
type Hub struct {
	clients map[string]*Client
	mu      sync.RWMutex

	// Callbacks
	OnJoin  func(client *Client)
	OnLeave func(clientID string)
}

// NewHub creates a new signaling hub.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[string]*Client),
	}
}

// Register adds a new WebSocket client and starts its read/write loops.
func (h *Hub) Register(id, role string, conn *websocket.Conn) *Client {
	client := &Client{
		ID:   id,
		Role: role,
		Conn: conn,
		Send: make(chan []byte, 64),
		hub:  h,
	}

	h.mu.Lock()
	h.clients[id] = client
	h.mu.Unlock()

	// Start write pump
	go client.writePump()
	// Start read pump
	go client.readPump()

	if h.OnJoin != nil {
		h.OnJoin(client)
	}

	return client
}

// Unregister removes a client.
func (h *Hub) Unregister(id string) {
	h.mu.Lock()
	client, exists := h.clients[id]
	if exists {
		delete(h.clients, id)
	}
	h.mu.Unlock()

	if exists {
		close(client.Send)
		client.Conn.Close()
		if h.OnLeave != nil {
			h.OnLeave(id)
		}
	}
}

// SendTo sends a message to a specific client.
func (h *Hub) SendTo(id string, msg Message) error {
	h.mu.RLock()
	client, exists := h.clients[id]
	h.mu.RUnlock()

	if !exists {
		return fmt.Errorf("client %s not found", id)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	select {
	case client.Send <- data:
		return nil
	default:
		return fmt.Errorf("client %s send buffer full", id)
	}
}

// Broadcast sends a message to all clients.
func (h *Hub) Broadcast(msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, client := range h.clients {
		select {
		case client.Send <- data:
		default:
			// Skip slow clients
		}
	}
}

// BroadcastExcept sends a message to all clients except one.
func (h *Hub) BroadcastExcept(msg Message, exceptID string) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for id, client := range h.clients {
		if id == exceptID {
			continue
		}
		select {
		case client.Send <- data:
		default:
		}
	}
}

// SendSystem sends a system message to all clients.
func (h *Hub) SendSystem(text string) {
	h.Broadcast(Message{
		Type:      MsgSystem,
		Text:      text,
		System:    true,
		Timestamp: time.Now().UnixMilli(),
	})
}

// ClientCount returns the number of connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// writePump pumps messages from the Send channel to the WebSocket connection.
func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second) // Ping interval
	defer ticker.Stop()
	defer c.Conn.Close()

	for {
		select {
		case message, ok := <-c.Send:
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// readPump reads messages from the WebSocket connection.
func (c *Client) readPump() {
	defer c.hub.Unregister(c.ID)

	c.Conn.SetReadLimit(65536)
	c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, data, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				// Log unexpected close
			}
			return
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		msg.Raw = data

		if c.OnMessage != nil {
			c.OnMessage(c, msg)
		}
	}
}
