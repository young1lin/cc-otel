package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
			"glm-5":       "claude-opus-4-6",
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

// ── v0.1.0 legacy-directory migration ──────────────────────────────────────

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// No legacy install: nothing happens, the new directory is used.
func TestMigrateLegacy_NoLegacyDir(t *testing.T) {
	base := t.TempDir()
	newDir := filepath.Join(base, "new")
	if !migrateLegacyDataDir(newDir, filepath.Join(base, "legacy"), "") {
		t.Error("expected new dir to be used when no legacy dir exists")
	}
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		t.Error("migrate must not create the new directory itself")
	}
}

// New directory already exists: a legacy install must never be touched.
func TestMigrateLegacy_NewDirExists(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "legacy")
	newDir := filepath.Join(base, "new")
	os.MkdirAll(legacy, 0o755)
	writeFile(t, filepath.Join(legacy, "cc-otel.db"), "old data")
	os.MkdirAll(newDir, 0o755)

	if !migrateLegacyDataDir(newDir, legacy, "") {
		t.Error("expected new dir to be used when it already exists")
	}
	data, err := os.ReadFile(filepath.Join(legacy, "cc-otel.db"))
	if err != nil || string(data) != "old data" {
		t.Errorf("legacy dir must be untouched, got %q err=%v", data, err)
	}
}

// The normal upgrade path: the whole directory moves, contents follow.
func TestMigrateLegacy_RenamesWholeDir(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "legacy")
	newDir := filepath.Join(base, "new")
	os.MkdirAll(legacy, 0o755)
	writeFile(t, filepath.Join(legacy, "cc-otel.db"), "the database")

	if !migrateLegacyDataDir(newDir, legacy, "") {
		t.Fatal("expected migration to succeed")
	}
	data, err := os.ReadFile(filepath.Join(newDir, "cc-otel.db"))
	if err != nil || string(data) != "the database" {
		t.Errorf("content did not follow the rename: %q err=%v", data, err)
	}
	if dirExists(legacy) {
		t.Error("legacy dir must be gone after the rename")
	}
}

// A rename that cannot succeed must fall back to the legacy directory — never
// strand the data behind a fresh start. The realistic trigger is the old
// daemon holding a database file open inside the legacy directory, which only
// blocks renames under Windows file-sharing semantics.
func TestMigrateLegacy_FallsBackWhenRenameFails(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("open-file directory locks need Windows sharing semantics")
	}
	base := t.TempDir()
	legacy := filepath.Join(base, "legacy")
	newDir := filepath.Join(base, "new")
	os.MkdirAll(legacy, 0o755)
	writeFile(t, filepath.Join(legacy, "cc-otel.db"), "the database")

	f, err := os.Open(filepath.Join(legacy, "cc-otel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if migrateLegacyDataDir(newDir, legacy, "") {
		t.Error("expected fallback to legacy when the rename is blocked")
	}
	data, err := os.ReadFile(filepath.Join(legacy, "cc-otel.db"))
	if err != nil || string(data) != "the database" {
		t.Errorf("legacy data must stay in place on fallback, got %q err=%v", data, err)
	}
}

// A stray non-directory at the target path must never be clobbered: on Windows
// os.Rename(dir, existingFile) would silently replace the file.
func TestMigrateLegacy_NeverClobbersStrayFile(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "legacy")
	newDir := filepath.Join(base, "new")
	os.MkdirAll(legacy, 0o755)
	writeFile(t, filepath.Join(legacy, "cc-otel.db"), "the database")
	writeFile(t, newDir, "user file that must survive")

	if migrateLegacyDataDir(newDir, legacy, "") {
		t.Error("expected fallback to legacy when a stray file occupies the target")
	}
	data, err := os.ReadFile(newDir)
	if err != nil || string(data) != "user file that must survive" {
		t.Errorf("stray file was clobbered: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(legacy, "cc-otel.db")); err != nil {
		t.Errorf("legacy data must stay in place: %v", err)
	}
}

// Running the binary from inside the legacy directory must skip migration
// (Windows cannot rename a directory containing the running executable).
func TestMigrateLegacy_SkipsWhenExeInsideLegacy(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "legacy")
	newDir := filepath.Join(base, "new")
	os.MkdirAll(filepath.Join(legacy, "sub"), 0o755)
	writeFile(t, filepath.Join(legacy, "sub", "cc-otel.exe"), "binary")

	if migrateLegacyDataDir(newDir, legacy, filepath.Join(legacy, "sub")) {
		t.Error("expected migration to be skipped when the exe runs from the legacy dir")
	}
	if !dirExists(legacy) {
		t.Error("legacy dir must still exist when migration is skipped")
	}
}

// The migrated config's absolute db_path must follow the rename, or the daemon
// cannot open the database it just moved.
func TestMigrateLegacy_RewritesConfigPaths(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "legacy")
	newDir := filepath.Join(base, "new")
	os.MkdirAll(legacy, 0o755)
	sep := string(filepath.Separator)
	writeFile(t, filepath.Join(legacy, "cc-otel.yaml"),
		"otel_port: 4317"+sep+"\ndb_path: "+legacy+sep+"cc-otel.db\n")

	if !migrateLegacyDataDir(newDir, legacy, "") {
		t.Fatal("expected migration to succeed")
	}
	cfg, err := os.ReadFile(filepath.Join(newDir, "cc-otel.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), legacy) {
		t.Errorf("config still references the legacy dir: %q", cfg)
	}
	if !strings.Contains(string(cfg), filepath.Join(newDir, "cc-otel.db")) {
		t.Errorf("config does not point at the new db: %q", cfg)
	}
}
func TestLoadRejectsMemoryDB(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mem.yaml")
	if err := os.WriteFile(p, []byte("db_path: \":memory:\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal(":memory: db_path must be rejected at load time")
	} else if !strings.Contains(err.Error(), ":memory:") {
		t.Fatalf("error should name :memory:, got %v", err)
	}
}

func TestLoadRejectsPortZero(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ports.yaml")
	if err := os.WriteFile(p, []byte("otel_port: 0\nweb_port: 18899\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("otel_port 0 must be rejected (binds a random ephemeral port while status reports the configured one)")
	}
}
