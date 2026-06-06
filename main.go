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
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	pion "github.com/pion/webrtc/v4"
	"github.com/rs/zerolog"

	"github.com/kiseding/syncwatch/internal/api"
	"github.com/kiseding/syncwatch/internal/auth"
	"github.com/kiseding/syncwatch/internal/config"
	"github.com/kiseding/syncwatch/internal/room"
	"github.com/kiseding/syncwatch/internal/signaling"
	"github.com/kiseding/syncwatch/internal/webrtc"
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
		defaultHash, _ := auth.HashPassword("syncwatch")
		logger.Info().Str("hash", defaultHash).Msg("no password set, using default: syncwatch")
		cfg.Auth.PasswordHash = defaultHash
	}

	// Auth
	tokenManager := auth.NewTokenManager(cfg.Auth.JWTSecret, cfg.Auth.SessionTimeout)
	rateLimiter := auth.NewRateLimiter()

	// ICE servers
	iceServers := buildICEServers(cfg)

	// Core components
	sfu := webrtc.NewSFU(iceServers, cfg.WebRTC.PublicIPs)
	hub := signaling.NewHub()
	room := room.NewRoom(sfu, hub, cfg.Media.FFmpegPath, cfg.Media.FFprobePath)
	room.SetTranscodeConfig(
		cfg.Transcode.VideoCodec,
		cfg.Transcode.AudioCodec,
		cfg.Transcode.VideoBitrate,
		cfg.Transcode.AudioBitrate,
		cfg.Transcode.FPS,
	)

	// HTTP mux
	mux := http.NewServeMux()

	// API routes (use router for readability)
	handler := api.NewHandler(room)
	apiRouter := api.NewRouter(handler, tokenManager, rateLimiter,
		cfg.Auth.RateLimitPerMin, cfg.Auth.PasswordHash, cfg.Auth.AdminPasswordHash)

	mux.HandleFunc("POST /api/auth", apiRouter.HandleAuth)
	mux.HandleFunc("POST /api/admin/auth", apiRouter.HandleAdminAuth)
	mux.Handle("POST /api/playback/play", apiRouter.HostOnly(handler.Play))
	mux.Handle("POST /api/playback/pause", apiRouter.HostOnly(handler.Pause))
	mux.Handle("POST /api/playback/resume", apiRouter.HostOnly(handler.Resume))
	mux.Handle("POST /api/playback/seek", apiRouter.HostOnly(handler.Seek))
	mux.Handle("POST /api/playback/speed", apiRouter.HostOnly(handler.SetSpeed))
	mux.Handle("POST /api/playback/audio", apiRouter.HostOnly(handler.SwitchAudio))
	mux.Handle("POST /api/playback/subtitle", apiRouter.HostOnly(handler.SwitchSubtitle))
	mux.Handle("GET /api/status", apiRouter.AuthRequired(handler.Status))
	mux.Handle("GET /api/media/info", apiRouter.AuthRequired(handler.MediaInfo))
	mux.Handle("GET /api/media/scan", apiRouter.AuthRequired(handler.MediaScan))
	mux.Handle("GET /api/state", apiRouter.AuthRequired(handler.SignalingState))

	// WebSocket signaling
	setupWebSocket(mux, tokenManager, sfu, hub, room, &logger)

	// Serve frontend
	serveFrontend(mux, &logger)

	// HTTP server
	server := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      api.CORSMiddleware(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
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

// buildICEServers converts config to pion ICE servers.
func buildICEServers(cfg *config.Config) []pion.ICEServer {
	var servers []pion.ICEServer
	for _, url := range cfg.WebRTC.STUNServers {
		servers = append(servers, pion.ICEServer{URLs: []string{url}})
	}
	for _, turn := range cfg.WebRTC.TURNServers {
		servers = append(servers, pion.ICEServer{
			URLs:       turn.URLs,
			Username:   turn.Username,
			Credential: turn.Credential,
		})
	}
	return servers
}

// setupWebSocket configures the WebSocket signaling endpoint.
func setupWebSocket(mux *http.ServeMux, tm *auth.TokenManager, sfu *webrtc.SFU,
	hub *signaling.Hub, r *room.Room, logger *zerolog.Logger) {

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	// Handle ICE candidates from server-side
	sfu.OnViewerJoin = func(session *webrtc.ViewerSession) {
		session.PeerConn.OnICECandidate(func(candidate *pion.ICECandidate) {
			if candidate == nil {
				return
			}
			c := candidate.ToJSON()
			hub.SendTo(session.ID, signaling.Message{
				Type:      signaling.MsgICECandidate,
				Candidate: c.Candidate,
				SDPMid:    *c.SDPMid,
				SDPMIndex: int(*c.SDPMLineIndex),
			})
		})
	}

	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, req *http.Request) {
		tokenStr := req.URL.Query().Get("token")
		claims, err := tm.Validate(tokenStr)
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			logger.Error().Err(err).Msg("ws upgrade failed")
			return
		}

		viewerID := sfu.GenerateViewerID()

		session, err := sfu.CreateSession(viewerID, claims.Role)
		if err != nil {
			logger.Error().Err(err).Str("viewer", viewerID).Msg("create session failed")
			conn.Close()
			return
		}

		// Register WS client
		client := hub.Register(viewerID, claims.Role, conn)

		// Attach viewer to room
		r.AddViewer(session)

		// Handle messages from this client
		hub.OnMessage = func(c *signaling.Client, msg signaling.Message) {
			if c.ID != viewerID {
				return
			}
			switch msg.Type {
			case signaling.MsgAnswer:
				answer := pion.SessionDescription{
					Type: pion.SDPTypeAnswer,
					SDP:  msg.SDP,
				}
				session.PeerConn.SetRemoteDescription(answer)

			case signaling.MsgICECandidate:
				candidateStr, ok := msg.Candidate.(string)
				if !ok {
					return
				}
				mid := msg.SDPMid
				candidateInit := pion.ICECandidateInit{
					Candidate: candidateStr,
				}
				if mid != "" {
					candidateInit.SDPMid = &mid
				}
				session.PeerConn.AddICECandidate(candidateInit)
			}
		}

		// Create and send SDP offer
		offer, err := session.PeerConn.CreateOffer(nil)
		if err != nil {
			logger.Error().Err(err).Str("viewer", viewerID).Msg("create offer failed")
			hub.Unregister(viewerID)
			return
		}
		session.PeerConn.SetLocalDescription(offer)

		hub.SendTo(viewerID, signaling.Message{
			Type: signaling.MsgOffer,
			SDP:  offer.SDP,
		})

		// Send current room state
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
		// Attach subtitle data if available
		if subFmt, subContent, subIdx := r.GetSubtitleData(); subIdx >= 0 {
			roomState.Subtitle = &signaling.SubtitleData{
				Format:  subFmt,
				Content: subContent,
				Index:   subIdx,
			}
		}
		// Audio tracks
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

		hub.SendTo(viewerID, signaling.Message{
			Type:      signaling.MsgJoined,
			RoomState: roomState,
		})

		hub.SendSystem(fmt.Sprintf("%s 加入了房间", viewerID))

		// Cleanup on disconnect
		defer func() {
			r.RemoveViewer(session)
			sfu.RemoveSession(viewerID)
			hub.SendSystem(fmt.Sprintf("%s 离开了房间", viewerID))
		}()

		_ = client // keep alive until disconnect (readPump handles cleanup)
		select {}
	})
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
// Paths matching static assets (with file extension) are served directly.
// All other paths fall back to index.html.
func spaHandler(fsys http.FileSystem) http.HandlerFunc {
	fileServer := http.FileServer(fsys)
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Static assets have file extensions — serve directly
		if hasExt(path) {
			fileServer.ServeHTTP(w, r)
			return
		}

		// SPA routes — try the file, fallback to index.html
		f, err := fsys.Open(path)
		if err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// Serve index.html for SPA routing
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	}
}

// hasExt checks if the path looks like a static file.
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
