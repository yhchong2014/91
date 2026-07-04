package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequiresAdminSetup(t *testing.T) {
	if !RequiresAdminSetup(&Config{Server: Server{Admin: Admin{Username: DefaultAdminUsername, Password: DefaultAdminPassword}}}) {
		t.Fatal("default admin credentials should require setup")
	}
	if RequiresAdminSetup(&Config{Server: Server{Admin: Admin{Username: "owner", Password: "secret123"}}}) {
		t.Fatal("custom admin credentials should not require setup")
	}
}

func TestWriteAdminCredentialsUpdatesConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
server:
  listen: "127.0.0.1:9192"
  admin:
    username: "admin"
    password: "admin123"
storage:
  db_path: "./data/video-site.db"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := WriteAdminCredentials(path, "owner", "new-secret"); err != nil {
		t.Fatalf("write admin credentials: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Server.Admin.Username != "owner" {
		t.Fatalf("username = %q, want owner", cfg.Server.Admin.Username)
	}
	if cfg.Server.Admin.Password != "new-secret" {
		t.Fatalf("password = %q, want new-secret", cfg.Server.Admin.Password)
	}
	if cfg.Server.Listen != "127.0.0.1:9192" {
		t.Fatalf("listen = %q, want preserved value", cfg.Server.Listen)
	}
	if cfg.Storage.DBPath != "./data/video-site.db" {
		t.Fatalf("db path = %q, want preserved value", cfg.Storage.DBPath)
	}
}

func TestLoadDefaultScannerVideoExtensionsIncludeSTRM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !hasVideoExtension(cfg.Scanner.VideoExtensions, ".strm") {
		t.Fatalf("video extensions = %#v, want .strm", cfg.Scanner.VideoExtensions)
	}
}

func TestLoadLegacyDefaultScannerVideoExtensionsIncludeSTRM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
scanner:
  video_extensions: [".mp4", ".mkv", ".mov", ".webm", ".avi"]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !hasVideoExtension(cfg.Scanner.VideoExtensions, ".strm") {
		t.Fatalf("video extensions = %#v, want .strm appended for legacy default list", cfg.Scanner.VideoExtensions)
	}
}

func TestLoadCustomScannerVideoExtensionsArePreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
scanner:
  video_extensions: [".mp4"]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Scanner.VideoExtensions) != 1 || cfg.Scanner.VideoExtensions[0] != ".mp4" {
		t.Fatalf("video extensions = %#v, want custom list preserved", cfg.Scanner.VideoExtensions)
	}
}

func TestLoadDefaultNightlyCronHour(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Nightly.CronHour != 1 {
		t.Fatalf("nightly cron hour = %d, want 1", cfg.Nightly.CronHour)
	}
}

func TestLoadInvalidNightlyCronHourFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
nightly:
  cron_hour: 25
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Nightly.CronHour != 1 {
		t.Fatalf("nightly cron hour = %d, want fallback 1", cfg.Nightly.CronHour)
	}
}

func hasVideoExtension(exts []string, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, ext := range exts {
		if strings.ToLower(strings.TrimSpace(ext)) == want {
			return true
		}
	}
	return false
}
