package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds all runtime configuration for cc-otel, loaded from YAML with env overrides.
type Config struct {
	BindHost      string                `yaml:"bind_host"`
	OTELPort      int                   `yaml:"otel_port"`
	WebPort       int                   `yaml:"web_port"`
	OTLPHTTPPort  int                   `yaml:"otlp_http_port"`
	DBPath        string                `yaml:"db_path"`
	RetentionDays int                   `yaml:"retention_days"`
	RawTTLDays    int                   `yaml:"raw_ttl_days"`
	ModelMapping  map[string]string     `yaml:"model_mapping"`
	Pricing       map[string]PriceEntry `yaml:"pricing"`

	// OpenRouterBaseURL overrides the API root used by the pricing suggest
	// lookup (mirror / self-hosted gateway). Empty keeps the documented default.
	OpenRouterBaseURL string `yaml:"openrouter_base_url"`
}

// DefaultOpenRouterBaseURL mirrors pricing.DefaultOpenRouterBaseURL (kept here
// so config defaults don't import the pricing package).
const DefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"

// PriceEntry is a user-supplied per-token price override for a single model.
// Costs are USD per token; cache fields fall back to multipliers (0.1×/1.25×) when zero.
type PriceEntry struct {
	Input         float64  `yaml:"input"`
	Output        float64  `yaml:"output"`
	CacheRead     float64  `yaml:"cache_read"`
	CacheCreation float64  `yaml:"cache_creation"`
	Aliases       []string `yaml:"aliases"`
}

// claudeDir returns ~/.claude (Claude Code's standard data directory). Only
// used as the legacy install location for the v0.1.0 migration below.
func claudeDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// ccOtelDir returns ~/.cc-otel, the client-agnostic home for cc-otel's binary,
// config, database and logs. cc-otel is no longer coupled to Claude Code.
func ccOtelDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".cc-otel")
}

// exeDir returns the directory of the running executable.
func exeDir() string {
	self, err := os.Executable()
	if err != nil || strings.TrimSpace(self) == "" {
		return ""
	}
	return filepath.Dir(self)
}

// defaultDataDir returns the directory for config/db/pid/log files.
//
// Priority:
//  1. Dev mode: exe is in a bin/ directory → use bin/ (local builds self-contained)
//  2. Default: ~/.cc-otel/ — migrated from the legacy ~/.claude/cc-otel/ on
//     first run (see migrateLegacyDataDir)
func defaultDataDir() string {
	// 1. Dev mode: exe in bin/ directory
	exe := exeDir()
	if exe != "" && strings.EqualFold(filepath.Base(exe), "bin") {
		return exe
	}

	// 2. Default: ~/.cc-otel/, auto-migrated from ~/.claude/cc-otel/
	dir := ccOtelDir()
	if dir == "" {
		return "."
	}
	legacy := filepath.Join(claudeDir(), "cc-otel")
	if claudeDir() == "" {
		legacy = ""
	}
	if migrateLegacyDataDir(dir, legacy, exe) || dirExists(dir) {
		_ = os.MkdirAll(dir, 0o755)
		return dir
	}
	// Migration could not run (typically the old daemon still holds the old
	// directory's files). Keep using the legacy location for this invocation —
	// never create a fresh empty database while the old one is stranded.
	_ = os.MkdirAll(legacy, 0o755)
	return legacy
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// fileExists reports whether path exists and is NOT a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// migrateLegacyDataDir moves the pre-v0.1.0 install (~/.claude/cc-otel) to the
// standalone ~/.cc-otel home. It reports whether the NEW directory should be
// used; on any failure it leaves everything untouched so defaultDataDir can
// fall back to the legacy location.
//
// Safety property: this function must never strand data — the only outcomes
// are "whole directory moved" or "nothing changed".
func migrateLegacyDataDir(newDir, legacyDir, exeDir string) bool {
	if legacyDir == "" || newDir == "" {
		return true // nothing to migrate from; use the new directory
	}
	if !dirExists(legacyDir) {
		if fileExists(newDir) {
			return false // a stray file occupies the name; the caller keeps a working location
		}
		return true // no legacy install
	}
	if dirExists(newDir) {
		return true // already migrated (or a fresh v0.1.0+ install)
	}
	if fileExists(newDir) {
		// os.Rename would happily replace a plain file with the directory on
		// Windows — never clobber whatever the user parked at this path.
		log.Printf("cc-otel: %s exists but is not a directory; keeping the install at %s", newDir, legacyDir)
		return false
	}
	// Renaming a directory that contains the running executable fails on
	// Windows, and the user is mid-upgrade anyway: skip and tell them how to
	// finish it.
	if exeDir != "" && (strings.EqualFold(exeDir, legacyDir) || isWithin(exeDir, legacyDir)) {
		log.Printf("cc-otel: running from the legacy directory %s; migration to %s is skipped — "+
			"run `cc-otel install` from a copy outside it", legacyDir, newDir)
		return false
	}
	err := os.Rename(legacyDir, newDir)
	if err == nil {
		rewriteMigratedConfig(newDir, legacyDir)
		log.Printf("cc-otel: migrated %s → %s (pre-v0.1.0 install location)", legacyDir, newDir)
		return true
	}
	// Another process may have completed the migration between our checks.
	if !dirExists(legacyDir) && dirExists(newDir) {
		return true
	}
	log.Printf("cc-otel: cannot migrate %s → %s (%v); continuing with the legacy directory. "+
		"Stop the running cc-otel daemon, then start it again to retry the migration.", legacyDir, newDir, err)
	return false
}

// isWithin reports whether path is inside dir (or equals it), case-insensitively
// for the Windows filesystems this runs on.
func isWithin(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// rewriteMigratedConfig re-points absolute paths inside the migrated
// cc-otel.yaml at its new home. A db_path written for the legacy directory
// would dangle after the rename and the daemon would fail to open the database
// it just moved.
func rewriteMigratedConfig(newDir, legacyDir string) {
	cfgPath := filepath.Join(newDir, "cc-otel.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return // no config file in the moved directory — nothing to rewrite
	}
	text := string(data)
	for _, pair := range [][2]string{
		{legacyDir, newDir}, // native separators (Windows backslash)
		{filepath.ToSlash(legacyDir), filepath.ToSlash(newDir)}, // forward-slash spelling
		{"~/.claude/cc-otel", "~/.cc-otel"},                     // tilde spelling
	} {
		text = strings.ReplaceAll(text, pair[0], pair[1])
	}
	if text == string(data) {
		return
	}
	if err := os.WriteFile(cfgPath, []byte(text), 0o644); err != nil {
		log.Printf("cc-otel: could not rewrite paths in the migrated config (%v) — edit db_path in %s manually", err, cfgPath)
	}
}

// DefaultConfigPath returns the absolute path to the default config file.
func DefaultConfigPath() string {
	return filepath.Join(defaultDataDir(), "cc-otel.yaml")
}

// DefaultDBPath returns the absolute path to the default SQLite database file.
func DefaultDBPath() string {
	return filepath.Join(defaultDataDir(), "cc-otel.db")
}

// DefaultPIDPath returns the absolute path to the daemon PID file.
func DefaultPIDPath() string {
	return filepath.Join(defaultDataDir(), "cc-otel.pid")
}

// DefaultBinDir returns the directory for the installed executable (same as data dir).
func DefaultBinDir() string {
	return defaultDataDir()
}

func DefaultWebPort() int      { return 8899 }
func DefaultOTELPort() int     { return 4317 }
func DefaultOTLPHTTPPort() int { return 4318 }

// Environment variable overrides (highest priority)
const (
	EnvOTELPort          = "CC_OTEL_OTEL_PORT"
	EnvWebPort           = "CC_OTEL_WEB_PORT"
	EnvOTLPHTTPPort      = "CC_OTEL_OTLP_HTTP_PORT"
	EnvDBPath            = "CC_OTEL_DB_PATH"
	EnvOpenRouterBaseURL = "CC_OTEL_OPENROUTER_BASE_URL"
)

// expandTilde replaces a leading ~/ or ~\ with the user's home directory.
func expandTilde(path string) string {
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// DefaultBindHost is the loopback default for bind_host. The daemon runs
// with zero authentication: an omitted bind_host must NOT fall back to the
// wildcard address — 0.0.0.0 would expose every mutating endpoint (2GB DB
// upload, one-click migration, pricing rewrite) to the whole LAN with no
// auth (R2 review A2-F1/A5-F1). Set bind_host: 0.0.0.0 explicitly for LAN
// access.
const DefaultBindHost = "127.0.0.1"

// Load reads the YAML config file at path, applies tilde expansion, then overrides with
// environment variables (CC_OTEL_OTEL_PORT, CC_OTEL_WEB_PORT, CC_OTEL_DB_PATH).
// Returns defaults if the file does not exist.
func Load(path string) (*Config, error) {
	cfg := &Config{
		BindHost:          DefaultBindHost,
		OTELPort:          4317,
		WebPort:           8899,
		OTLPHTTPPort:      DefaultOTLPHTTPPort(),
		DBPath:            DefaultDBPath(),
		RetentionDays:     90,
		RawTTLDays:        5,
		OpenRouterBaseURL: DefaultOpenRouterBaseURL,
	}

	if path == "" {
		applyEnvOverrides(cfg)
		if err := cfg.validate(); err != nil {
			return nil, err
		}
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			applyEnvOverrides(cfg)
			if err := cfg.validate(); err != nil {
				return nil, err
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.DBPath = expandTilde(cfg.DBPath)
	applyEnvOverrides(cfg)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate rejects configs that would misbehave silently at runtime:
// a :memory: database vanishes with the process (and defeats every
// persistence expectation, including the migration button), and port 0
// for otel_port/web_port makes the listener bind a RANDOM OS-assigned
// ephemeral port while /api/status keeps reporting the configured number —
// exporters and users would chase a port nothing listens on (R3 config
// review F1/F3, wording corrected in the R4 docs pass: only otlp_http_port
// 0 disables by design).
func (c *Config) validate() error {
	if strings.TrimSpace(c.BindHost) == "" {
		c.BindHost = DefaultBindHost
	}
	if strings.TrimSpace(c.DBPath) == ":memory:" {
		return fmt.Errorf("config: db_path ':memory:' is not supported — point db_path at a real file")
	}
	if c.OTELPort <= 0 || c.OTELPort > 65535 {
		return fmt.Errorf("config: otel_port %d out of range (1-65535)", c.OTELPort)
	}
	if c.WebPort <= 0 || c.WebPort > 65535 {
		return fmt.Errorf("config: web_port %d out of range (1-65535)", c.WebPort)
	}
	if c.OTLPHTTPPort < 0 || c.OTLPHTTPPort > 65535 {
		return fmt.Errorf("config: otlp_http_port %d out of range (0 disables, 1-65535 binds)", c.OTLPHTTPPort)
	}
	// Negative retention silently meant keep-everything everywhere (Cleanup,
	// migration sanity both guard on >0) — reject so the misconfiguration is
	// loud instead of a surprise audit gap (R4 integrity F5). 0 keeps its
	// documented all-time semantics for tests.
	if c.RetentionDays < 0 {
		return fmt.Errorf("config: retention_days %d is negative (0 = keep everything)", c.RetentionDays)
	}
	return nil
}

func applyEnvOverrides(cfg *Config) {
	if cfg == nil {
		return
	}
	if v := strings.TrimSpace(os.Getenv(EnvOTELPort)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 65535 {
			cfg.OTELPort = n
		}
	}
	if v := strings.TrimSpace(os.Getenv(EnvWebPort)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 65535 {
			cfg.WebPort = n
		}
	}
	if v := strings.TrimSpace(os.Getenv(EnvOTLPHTTPPort)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 65535 {
			cfg.OTLPHTTPPort = n
		}
	}
	if v := strings.TrimSpace(os.Getenv(EnvDBPath)); v != "" {
		cfg.DBPath = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvOpenRouterBaseURL)); v != "" {
		cfg.OpenRouterBaseURL = v
	}
}

// ResolveActualModel looks up the user's actual model name from the Anthropic model name.
func (c *Config) ResolveActualModel(anthropicModel string) string {
	am := strings.TrimSpace(anthropicModel)
	for actual, anthropic := range c.ModelMapping {
		if strings.EqualFold(strings.TrimSpace(anthropic), am) {
			return actual
		}
	}
	return ""
}
