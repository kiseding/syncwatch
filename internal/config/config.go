package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds all application configuration.
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Room      RoomConfig      `yaml:"room"`
	WebRTC    WebRTCConfig    `yaml:"webrtc"`
	Media     MediaConfig     `yaml:"media"`
	Auth      AuthConfig      `yaml:"auth"`
	Transcode TranscodeConfig `yaml:"transcode"`
}

type ServerConfig struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	PublicURL string `yaml:"public_url"`
	TLS       bool   `yaml:"tls"`
	CertFile  string `yaml:"cert_file"`
	KeyFile   string `yaml:"key_file"`
}

type RoomConfig struct {
	DefaultPassword string `yaml:"default_password"`
	MaxViewers      int    `yaml:"max_viewers"`
	SessionTimeout  string `yaml:"session_timeout"`
}

type WebRTCConfig struct {
	ICELite      bool           `yaml:"ice_lite"`
	UDPPortStart int            `yaml:"udp_port_start"`
	UDPPortEnd   int            `yaml:"udp_port_end"`
	STUNServers  []string       `yaml:"stun_servers"`
	TURNServers  []TURNConfig   `yaml:"turn_servers"`
	IPv6First    bool           `yaml:"ipv6_first"`
	PublicIPs    []string       `yaml:"public_ips"`
}

type TURNConfig struct {
	URLs       []string `yaml:"urls"`
	Username   string   `yaml:"username"`
	Credential string   `yaml:"credential"`
}

type MediaConfig struct {
	FFmpegPath           string   `yaml:"ffmpeg_path"`
	FFprobePath          string   `yaml:"ffprobe_path"`
	ScanDirs             []string `yaml:"scan_dirs"`
	AllowedExtensions    []string `yaml:"allowed_extensions"`
	HardwareAcceleration bool     `yaml:"hardware_acceleration"`
}

type TranscodeConfig struct {
	VideoCodec    string `yaml:"video_codec"`
	AudioCodec    string `yaml:"audio_codec"`
	VideoBitrate  string `yaml:"video_bitrate"`
	AudioBitrate  string `yaml:"audio_bitrate"`
	MaxResolution string `yaml:"max_resolution"`
	FPS           int    `yaml:"fps"`
	Preset        string `yaml:"preset"`
}

type AuthConfig struct {
	PasswordHash      string `yaml:"password_hash"`
	AdminPasswordHash string `yaml:"admin_password_hash"`
	RateLimitPerMin   int    `yaml:"rate_limit_per_min"`
	SessionTimeout    int    `yaml:"session_timeout"`
	JWTSecret         string `yaml:"jwt_secret"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8080,
		},
		Room: RoomConfig{
			MaxViewers:     50,
			SessionTimeout: "5m",
		},
		WebRTC: WebRTCConfig{
			ICELite:      true,
			UDPPortStart: 60000,
			UDPPortEnd:   60100,
			STUNServers: []string{
				"stun:stun.l.google.com:19302",
			},
			IPv6First: true,
		},
		Media: MediaConfig{
			FFmpegPath:        "ffmpeg",
			FFprobePath:       "ffprobe",
			AllowedExtensions: []string{".mp4", ".mkv", ".avi", ".mov", ".webm"},
		},
		Transcode: TranscodeConfig{
			VideoCodec:    "libvpx",
			AudioCodec:    "libopus",
			VideoBitrate:  "2000k",
			AudioBitrate:  "128k",
			MaxResolution: "1920x1080",
			FPS:           30,
			Preset:        "veryfast",
		},
		Auth: AuthConfig{
			RateLimitPerMin: 5,
			SessionTimeout:  86400,
		},
	}
}

// Load reads config from a YAML file, applying defaults for missing values.
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return defaults if no config file
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	// Generate a random JWT secret if not configured
	if cfg.Auth.JWTSecret == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err == nil {
			cfg.Auth.JWTSecret = hex.EncodeToString(secret)
		}
	}

	return cfg, nil
}
