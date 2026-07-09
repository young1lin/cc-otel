package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultConfig(t *testing.T) {
	f := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(f, []byte(`
otel_port: 4317
web_port: 8899
db_path: ./test.db
model_mapping:
  "glm-5": "claude-opus-4-6"
`), 0644)

	cfg, err := Load(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OTELPort != 4317 {
		t.Errorf("expected OTELPort 4317, got %d", cfg.OTELPort)
	}
	if cfg.WebPort != 8899 {
		t.Errorf("expected WebPort 8899, got %d", cfg.WebPort)
	}
	if cfg.DBPath != "./test.db" {
		t.Errorf("expected DBPath ./test.db, got %s", cfg.DBPath)
	}
}

func TestResolveActualModel(t *testing.T) {
	cfg := &Config{
		ModelMapping: map[string]string{
			"glm-5":      "claude-opus-4-6",
			"deepseek-r1": "claude-sonnet-4-6",
		},
	}

	tests := []struct {
		anthropic string
		want      string
	}{
		{"claude-opus-4-6", "glm-5"},
		{"CLAUDE-OPUS-4-6", "glm-5"},
		{"claude-sonnet-4-6", "deepseek-r1"},
		{"claude-haiku-4-5-20251001", ""},
	}

	for _, tt := range tests {
		got := cfg.ResolveActualModel(tt.anthropic)
		if got != tt.want {
			t.Errorf("ResolveActualModel(%q) = %q, want %q", tt.anthropic, got, tt.want)
		}
	}
}

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}

	cfgFile := filepath.Join(t.TempDir(), "tilde.yaml")
	os.WriteFile(cfgFile, []byte("db_path: ~/mydata/test.db\n"), 0644)

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	expected := filepath.Join(home, "mydata", "test.db")
	if cfg.DBPath != expected {
		t.Errorf("expected DBPath=%q, got %q", expected, cfg.DBPath)
	}

	// Must not start with "~"
	if len(cfg.DBPath) > 0 && cfg.DBPath[0] == '~' {
		t.Errorf("DBPath still starts with tilde: %q", cfg.DBPath)
	}
}

func TestRetentionDaysDefault(t *testing.T) {
	// Load with empty path to get defaults (no file)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.RetentionDays != 90 {
		t.Errorf("expected RetentionDays=90, got %d", cfg.RetentionDays)
	}
}

func TestRetentionDaysDefaultFromFile(t *testing.T) {
	// Load a config file that does not set retention_days
	cfgFile := filepath.Join(t.TempDir(), "no_retention.yaml")
	os.WriteFile(cfgFile, []byte("otel_port: 4317\nweb_port: 8899\n"), 0644)

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	// yaml.v3 Unmarshal preserves pre-set struct fields when absent from YAML,
	// so the default 90 should be retained.
	if cfg.RetentionDays != 90 {
		t.Errorf("expected RetentionDays=90, got %d", cfg.RetentionDays)
	}
}
