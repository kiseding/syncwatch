package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
		logger.Info().Str("hash", defaultHash).Msg("no password set, using default: syncwatch")
		cfg.Auth.PasswordHash = defaultHash
	}

	// Ensure upload dir exists
	if err := os.MkdirAll(cfg.Media.UploadDir, 0755); err != nil {
		logger.Fatal().Err(err).Msg("failed to create upload dir")
	}

	// Auth
	tokenManager := auth.NewTokenManager(cfg.Auth.JWTSecret, cfg.Auth.SessionTimeout)
	rateLimiter := auth.NewRateLimiter()

	// Server base URL for constructing media URLs
	publicURL := cfg.Server.PublicURL
	if publicURL == "" {
		publicURL = fmt.Sprintf("http://%s:%d", cfg.Server.Host, cfg.Server.Port)
	}

	// Core components
	hub := signaling.NewHub()
	room := room.NewRoom(hub, publicURL, cfg.Media.UploadDir)

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
	mux.Handle("GET /api/media/info", apiRouter.AuthRequired(handler.MediaInfo))
	mux.Handle("GET /api/media/scan", apiRouter.AuthRequired(handler.MediaScan))
	mux.Handle("GET /api/state", apiRouter.AuthRequired(handler.SignalingState))

	// Debug endpoint
	mux.HandleFunc("GET /api/debug", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"state":"%s","position":%.1f,"media_url":"%s"}`,
			room.State().String(), room.GetPosition(), room.GetMediaURL())
	})

	// WebSocket signaling
	setupWebSocket(mux, tokenManager, hub, room, &logger)

	// Serve frontend
	serveFrontend(mux, &logger)

	// HTTP server
	server := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      api.CORSMiddleware(mux),
		ReadTimeout:  60 * time.Second, // Allow large uploads
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
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
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatal().Err(err).Msg("server failed")
	}
	logger.Info().Msg("server stopped")
}

// setupWebSocket configures the WebSocket signaling endpoint.
func setupWebSocket(mux *http.ServeMux, tm *auth.TokenManager,
	hub *signaling.Hub, r *room.Room, logger *zerolog.Logger) {

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
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

		// Register WS client
		client := hub.Register(viewerID, claims.Role, conn)

		// Send current room state (joined message)
		sendJoined(client, r)

		// Handle messages from this client
		client.OnMessage = func(c *signaling.Client, msg signaling.Message) {
			logger.Debug().Str("viewer", c.ID).Str("type", msg.Type).Msg("ws msg")
		}

		displayName := viewerID
		if claims.Role == "host" {
			displayName = "Host"
		}
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
