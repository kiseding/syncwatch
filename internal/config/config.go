package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds all application configuration.
type Config struct {
	Server ServerConfig `yaml:"server"`
	Media  MediaConfig  `yaml:"media"`
	Auth   AuthConfig   `yaml:"auth"`
}

type ServerConfig struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	PublicURL string `yaml:"public_url"`
	TLS       bool   `yaml:"tls"`
	CertFile  string `yaml:"cert_file"`
	KeyFile   string `yaml:"key_file"`
}

type MediaConfig struct {
	ScanDirs          []string `yaml:"scan_dirs"`
	AllowedExtensions []string `yaml:"allowed_extensions"`
	UploadDir         string   `yaml:"upload_dir"`
}

type AuthConfig struct {
	Password          string `yaml:"password"`
	AdminPassword     string `yaml:"admin_password"`
	PasswordHash      string `yaml:"password_hash"`
	AdminPasswordHash string `yaml:"admin_password_hash"`
	RateLimitPerMin   int    `yaml:"rate_limit_per_min"`
	SessionTimeout    int    `yaml:"session_timeout"`
	JWTSecret         string `yaml:"jwt_secret"`
}

func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8080,
		},
		Media: MediaConfig{
			AllowedExtensions: []string{".mp4", ".mkv", ".avi", ".mov", ".webm"},
			UploadDir:         "/tmp/syncwatch_uploads",
		},
		Auth: AuthConfig{
			RateLimitPerMin: 5,
			SessionTimeout:  86400,
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	if cfg.Auth.JWTSecret == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err == nil {
			cfg.Auth.JWTSecret = hex.EncodeToString(secret)
		}
	}

	if cfg.Media.UploadDir == "" {
		cfg.Media.UploadDir = "/tmp/syncwatch_uploads"
	}

	return cfg, nil
}
