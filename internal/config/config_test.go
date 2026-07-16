package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingConfigAppliesRuntimeDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.JWTSecret == "" {
		t.Fatal("JWT secret was not generated")
	}
	if cfg.Media.UploadDir != "data/uploads" {
		t.Fatalf("unexpected upload dir: %q", cfg.Media.UploadDir)
	}
	if cfg.Server.Port != 8080 || cfg.Auth.SessionTimeout != 86400 {
		t.Fatalf("defaults not applied: %#v", cfg)
	}
}

func TestLoadOverridesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("server:\n  port: 9090\nmedia:\n  upload_dir: custom\nauth:\n  jwt_secret: fixed\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 9090 || cfg.Media.UploadDir != "custom" || cfg.Auth.JWTSecret != "fixed" {
		t.Fatalf("overrides not loaded: %#v", cfg)
	}
}
