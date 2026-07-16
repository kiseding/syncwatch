package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/kiseding/syncwatch/internal/api"
	"github.com/kiseding/syncwatch/internal/auth"
	"github.com/kiseding/syncwatch/internal/config"
	"github.com/kiseding/syncwatch/internal/room"
	"github.com/kiseding/syncwatch/internal/signaling"
)

//go:embed all:web/dist
var webFS embed.FS

var configPath = flag.String("config", "config.yaml", "path to config file")

func main() {
	flag.Parse()

	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).
		With().Timestamp().Logger()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to load config")
	}

	// Auto-hash plain-text passwords
	if cfg.Auth.Password != "" {
		hash, err := auth.HashPassword(cfg.Auth.Password)
		if err != nil {
			logger.Fatal().Err(err).Msg("failed to hash password")
		}
		cfg.Auth.PasswordHash = hash
	}
	if cfg.Auth.AdminPassword != "" {
		hash, err := auth.HashPassword(cfg.Auth.AdminPassword)
		if err != nil {
			logger.Fatal().Err(err).Msg("failed to hash admin password")
		}
		cfg.Auth.AdminPasswordHash = hash
	}
	if cfg.Auth.PasswordHash == "" {
		defaultHash, err := auth.HashPassword("syncwatch")
		if err != nil {
			logger.Fatal().Err(err).Msg("failed to generate default password hash")
		}
		logger.Warn().Msg("no password set, using default password: syncwatch")
		cfg.Auth.PasswordHash = defaultHash
	}

	// Ensure upload dir exists
	if err := os.MkdirAll(cfg.Media.UploadDir, 0755); err != nil {
		logger.Fatal().Err(err).Msg("failed to create upload dir")
	}

	// Auth
	tokenManager := auth.NewTokenManager(cfg.Auth.JWTSecret, cfg.Auth.SessionTimeout)
	rateLimiter := auth.NewRateLimiter()
	defer rateLimiter.Stop()

	// Core components
	hub := signaling.NewHub()
	room := room.NewRoom(hub, cfg.Media.UploadDir)

	// HTTP mux
	mux := http.NewServeMux()

	// API
	handler := api.NewHandler(room)
	handler.AllowedExts = cfg.Media.AllowedExtensions
	handler.UploadDir = cfg.Media.UploadDir
	handler.FFprobePath = "ffprobe"
	handler.ScanDirs = cfg.Media.ScanDirs
	apiRouter := api.NewRouter(tokenManager, rateLimiter,
		cfg.Auth.RateLimitPerMin, cfg.Auth.PasswordHash, cfg.Auth.AdminPasswordHash)

	mux.HandleFunc("POST /api/auth", apiRouter.HandleAuth)
	mux.HandleFunc("POST /api/admin/auth", apiRouter.HandleAdminAuth)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("POST /api/playback/play", apiRouter.HostOnly(handler.Play))
	mux.Handle("POST /api/playback/pause", apiRouter.HostOnly(handler.Pause))
	mux.Handle("POST /api/playback/resume", apiRouter.HostOnly(handler.Resume))
	mux.Handle("POST /api/playback/seek", apiRouter.HostOnly(handler.Seek))
	mux.Handle("POST /api/playback/speed", apiRouter.HostOnly(handler.SetSpeed))
	mux.Handle("POST /api/playback/sync", apiRouter.HostOnly(handler.SyncPosition))
	mux.Handle("POST /api/playback/audio", apiRouter.HostOnly(handler.SwitchAudio))
	mux.Handle("POST /api/playback/subtitle", apiRouter.HostOnly(handler.SwitchSubtitle))
	mux.Handle("POST /api/upload", apiRouter.HostOnly(handler.Upload))
	mux.Handle("POST /api/upload/subtitle", apiRouter.HostOnly(handler.UploadSubtitle))
	mux.Handle("GET /api/media/file", apiRouter.AuthRequired(handler.ServeFile))
	mux.Handle("GET /api/status", apiRouter.AuthRequired(handler.Status))
	mux.Handle("GET /api/media/info", apiRouter.HostOnly(handler.MediaInfo))
	mux.Handle("GET /api/media/scan", apiRouter.HostOnly(handler.MediaScan))
	mux.Handle("GET /api/state", apiRouter.AuthRequired(handler.SignalingState))

	// Diagnostics are host-only because they include the current media location.
	mux.Handle("GET /api/debug", apiRouter.HostOnly(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"state": room.State().String(), "position": room.GetPosition(),
			"media_url": room.GetMediaURL(), "ws_clients": hub.ClientCount(),
		})
	}))

	// WebSocket signaling
	setupWebSocket(mux, tokenManager, hub, room, &logger)

	// Serve frontend
	serveFrontend(mux, &logger)

	// HTTP server
	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:           api.CORSMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		logger.Info().Msg("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	logger.Info().Str("addr", server.Addr).Msg("syncwatch server starting")
	var serveErr error
	if cfg.Server.TLS {
		if cfg.Server.CertFile == "" || cfg.Server.KeyFile == "" {
			logger.Fatal().Msg("TLS is enabled but cert_file or key_file is empty")
		}
		serveErr = server.ListenAndServeTLS(cfg.Server.CertFile, cfg.Server.KeyFile)
	} else {
		serveErr = server.ListenAndServe()
	}
	if serveErr != nil && serveErr != http.ErrServerClosed {
		logger.Fatal().Err(serveErr).Msg("server failed")
	}
	logger.Info().Msg("server stopped")
}

// setupWebSocket configures the WebSocket signaling endpoint.
func setupWebSocket(mux *http.ServeMux, tm *auth.TokenManager,
	hub *signaling.Hub, r *room.Room, logger *zerolog.Logger) {

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true
			}
			u, err := url.Parse(origin)
			return err == nil && strings.EqualFold(u.Host, r.Host)
		},
	}

	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, req *http.Request) {
		tokenStr := req.URL.Query().Get("token")
		claims, err := tm.Validate(tokenStr)
		if err != nil {
			logger.Warn().Err(err).Str("remote", req.RemoteAddr).Msg("ws auth failed")
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			logger.Error().Err(err).Str("remote", req.RemoteAddr).Msg("ws upgrade failed")
			return
		}

		viewerID := fmt.Sprintf("viewer-%d", time.Now().UnixNano())
		logger.Info().Str("viewer", viewerID).Str("role", claims.Role).Str("remote", req.RemoteAddr).Msg("ws connected")

		displayName := viewerID
		if claims.Role == "host" {
			displayName = "Host"
		}

		// Chat is the only client-originated room message.
		onMessage := func(c *signaling.Client, msg signaling.Message) {
			if msg.Type != signaling.MsgChat {
				return
			}
			text := strings.TrimSpace(msg.Text)
			if text == "" {
				return
			}
			if len([]rune(text)) > 500 {
				text = string([]rune(text)[:500])
			}
			hub.Broadcast(signaling.Message{
				Type: signaling.MsgChat, Text: text, From: displayName,
				Timestamp: time.Now().UnixMilli(),
			})
		}

		// Register WS client, then send its initial room snapshot.
		client := hub.Register(viewerID, claims.Role, conn, onMessage)
		sendJoined(client, r)
		hub.SendSystem(fmt.Sprintf("%s 加入了房间", displayName))

		// Cleanup on disconnect
		defer func() {
			hub.SendSystem(fmt.Sprintf("%s 离开了房间", displayName))
		}()
		<-client.Done
	})
}

// sendJoined sends the current room state to a newly connected client.
func sendJoined(client *signaling.Client, r *room.Room) {
	roomState := &signaling.RoomState{
		State:    r.State().String(),
		Position: r.GetPosition(),
		Speed:    r.GetSpeed(),
	}
	if info := r.GetMediaInfo(); info != nil {
		roomState.Media = &signaling.MediaState{
			Filename: info.Path,
			Duration: info.Duration,
		}
	}
	if subFmt, subContent, subIdx := r.GetSubtitleData(); subIdx >= 0 {
		roomState.Subtitle = &signaling.SubtitleData{
			Format:  subFmt,
			Content: subContent,
			Index:   subIdx,
		}
	}
	var audioTracks []signaling.TrackInfo
	for _, t := range r.GetAudioTracks() {
		audioTracks = append(audioTracks, signaling.TrackInfo{
			Index: t.Index, Type: "audio",
			Language: t.Language, Title: t.Title,
		})
	}
	roomState.AudioTracks = audioTracks
	roomState.SelectedAudio = r.GetAudioIndex()
	roomState.SelectedSub = r.GetSubIndex()

	var subTracks []signaling.TrackInfo
	for _, s := range r.GetSubtitles() {
		name := filepath.Base(s.Path)
		subTracks = append(subTracks, signaling.TrackInfo{
			Index: s.Index, Type: "subtitle",
			Language: s.Language, Title: name + " (" + s.Format + ")",
		})
	}
	roomState.SubTracks = subTracks

	hub := r.Hub()
	hub.SendTo(client.ID, signaling.Message{
		Type:      signaling.MsgJoined,
		RoomState: roomState,
	})

	// Also send current media URL
	if url := r.GetMediaURL(); url != "" {
		hub.SendTo(client.ID, signaling.Message{
			Type:     signaling.MsgMedia,
			MediaURL: url,
		})
	}
}

// serveFrontend serves the embedded or filesystem frontend with SPA fallback.
func serveFrontend(mux *http.ServeMux, logger *zerolog.Logger) {
	webContent, err := fs.Sub(webFS, "web/dist")
	if err != nil {
		logger.Warn().Msg("embedded web not found, serving from filesystem")
		mux.HandleFunc("/", spaHandler(http.Dir("web/dist")))
	} else {
		mux.HandleFunc("/", spaHandler(http.FS(webContent)))
	}
}

// spaHandler returns a handler that serves static files with SPA fallback.
func spaHandler(fsys http.FileSystem) http.HandlerFunc {
	fileServer := http.FileServer(fsys)
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if hasExt(path) {
			fileServer.ServeHTTP(w, r)
			return
		}
		f, err := fsys.Open(path)
		if err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	}
}

func hasExt(path string) bool {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return i > 0 && path[i-1] != '/'
		}
		if path[i] == '/' {
			return false
		}
	}
	return false
}
