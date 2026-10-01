package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "ims.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoad(t *testing.T) {
	cfg, err := Load(writeConfig(t, "api:\n  address: 127.0.0.1\n  port: 8080\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := Config{API: API{Address: netip.MustParseAddr("127.0.0.1"), Port: 8080}}
	if cfg != want {
		t.Fatalf("Load = %+v, want %+v", cfg, want)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, "api:\n  address: 127.0.0.1\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.API.Port != defaultAPIPort {
		t.Fatalf("api.port = %d, want %d", cfg.API.Port, defaultAPIPort)
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"missing api address", "api:\n  port: 8080\n", "api.address is required"},
		{"port out of range", "api:\n  address: 127.0.0.1\n  port: 70000\n", "api.port 70000 is out of range"},
		{"unknown field", "api:\n  address: 127.0.0.1\nfoo: bar\n", "field foo not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.content))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("Load succeeded for a missing file")
	}
}
