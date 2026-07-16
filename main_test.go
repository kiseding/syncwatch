package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/kiseding/syncwatch/internal/auth"
	"github.com/kiseding/syncwatch/internal/room"
	"github.com/kiseding/syncwatch/internal/signaling"
)

func TestWebSocketJoinAndChat(t *testing.T) {
	tm := auth.NewTokenManager("test-secret", 60)
	token, err := tm.Generate("viewer")
	if err != nil {
		t.Fatal(err)
	}
	hub := signaling.NewHub()
	r := room.NewRoom(hub, t.TempDir())
	r.SetMedia("https://example.com/movie.mp4", room.SourceURL)
	mux := http.NewServeMux()
	logger := zerolog.Nop()
	setupWebSocket(mux, tm, hub, r, &logger)
	server := httptest.NewServer(mux)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	joined := readUntilType(t, conn, signaling.MsgJoined)
	if joined.RoomState == nil || joined.RoomState.State != "playing" {
		t.Fatalf("bad joined state: %#v", joined.RoomState)
	}
	mediaMessage := readUntilType(t, conn, signaling.MsgMedia)
	if mediaMessage.MediaURL != "https://example.com/movie.mp4" {
		t.Fatalf("bad media message: %#v", mediaMessage)
	}

	if err := conn.WriteJSON(signaling.Message{Type: signaling.MsgChat, Text: " hello "}); err != nil {
		t.Fatal(err)
	}
	chat := readUntilType(t, conn, signaling.MsgChat)
	if chat.Text != "hello" || chat.From == "" || chat.Timestamp == 0 {
		t.Fatalf("bad chat message: %#v", chat)
	}

	conn.Close()
	deadline := time.Now().Add(time.Second)
	for hub.ClientCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hub.ClientCount() != 0 {
		t.Fatal("WebSocket client was not unregistered")
	}
}

func readUntilType(t *testing.T, conn *websocket.Conn, messageType string) signaling.Message {
	t.Helper()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var message signaling.Message
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatal(err)
		}
		if message.Type == messageType {
			return message
		}
	}
}
