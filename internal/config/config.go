package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds all runtime configuration for cc-otel, loaded from YAML with env overrides.
type Config struct {
	OTELPort      int               `yaml:"otel_port"`
	WebPort       int               `yaml:"web_port"`
	DBPath        string            `yaml:"db_path"`
	RetentionDays int               `yaml:"retention_days"`
	ModelMapping  map[string]string `yaml:"model_mapping"`
}

// claudeDir returns ~/.claude (Claude Code's standard data directory).
func claudeDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude")
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
//  2. Legacy: ~/.claude/cc-otel.yaml or cc-otel.db exists → use ~/.claude/ (backward compat)
//  3. Default: ~/.claude/cc-otel/ (clean subdirectory layout for new installs)
func defaultDataDir() string {
	// 1. Dev mode: exe in bin/ directory
	exe := exeDir()
	if exe != "" && strings.EqualFold(filepath.Base(exe), "bin") {
		return exe
	}

	// 2. Legacy: flat files in ~/.claude/
	claude := claudeDir()
	if claude != "" {
		if _, err := os.Stat(filepath.Join(claude, "cc-otel.yaml")); err == nil {
			return claude
		}
		if _, err := os.Stat(filepath.Join(claude, "cc-otel.db")); err == nil {
			return claude
		}
	}

	// 3. Default: ~/.claude/cc-otel/
	if claude != "" {
		dir := filepath.Join(claude, "cc-otel")
		_ = os.MkdirAll(dir, 0o755)
		return dir
	}

	return "."
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

func DefaultWebPort() int  { return 8899 }
func DefaultOTELPort() int { return 4317 }

// Environment variable overrides (highest priority)
const (
	EnvOTELPort = "CC_OTEL_OTEL_PORT"
	EnvWebPort  = "CC_OTEL_WEB_PORT"
	EnvDBPath   = "CC_OTEL_DB_PATH"
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

// Load reads the YAML config file at path, applies tilde expansion, then overrides with
// environment variables (CC_OTEL_OTEL_PORT, CC_OTEL_WEB_PORT, CC_OTEL_DB_PATH).
// Returns defaults if the file does not exist.
func Load(path string) (*Config, error) {
	cfg := &Config{
		OTELPort:      4317,
		WebPort:       8899,
		DBPath:        DefaultDBPath(),
		RetentionDays: 90,
	}

	if path == "" {
		applyEnvOverrides(cfg)
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			applyEnvOverrides(cfg)
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.DBPath = expandTilde(cfg.DBPath)
	applyEnvOverrides(cfg)
	return cfg, nil
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
	if v := strings.TrimSpace(os.Getenv(EnvDBPath)); v != "" {
		cfg.DBPath = v
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
