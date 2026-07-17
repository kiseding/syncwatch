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
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	TLS      bool   `yaml:"tls"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
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
			UploadDir:         "data/uploads",
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
		if !os.IsNotExist(err) {
			return nil, err
		}
	} else {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
	}

	applyEnvironmentOverrides(cfg)

	if cfg.Auth.JWTSecret == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err == nil {
			cfg.Auth.JWTSecret = hex.EncodeToString(secret)
		}
	}

	if cfg.Media.UploadDir == "" {
		cfg.Media.UploadDir = "data/uploads"
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port <= 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Auth.RateLimitPerMin <= 0 {
		cfg.Auth.RateLimitPerMin = 5
	}
	if cfg.Auth.SessionTimeout <= 0 {
		cfg.Auth.SessionTimeout = 86400
	}

	return cfg, nil
}

func applyEnvironmentOverrides(cfg *Config) {
	if value := os.Getenv("SYNCWATCH_VIEWER_PASSWORD"); value != "" {
		cfg.Auth.Password = value
	}
	if value := os.Getenv("SYNCWATCH_ADMIN_PASSWORD"); value != "" {
		cfg.Auth.AdminPassword = value
	}
	if value := os.Getenv("SYNCWATCH_JWT_SECRET"); value != "" {
		cfg.Auth.JWTSecret = value
	}
}
