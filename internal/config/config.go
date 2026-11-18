// Package config loads mywa settings from environment variables.
package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Addr     string     // MYWA_ADDR, default ":8080"
	APIKey   string     // MYWA_API_KEY, required
	DBPath   string     // MYWA_DB_PATH, default "mywa.db"
	MediaDir string     // MYWA_MEDIA_DIR, default "media" next to the database
	LogLevel slog.Level // MYWA_LOG_LEVEL: debug|info|warn|error, default info
}

func Load() (Config, error) {
	c := Config{
		Addr: env("MYWA_ADDR", ":8080"),
		// Trimmed so a stray space or newline (e.g. `set MYWA_API_KEY=abc ` in cmd) doesn't
		// become part of the key; the web page trims what the user types the same way.
		APIKey: strings.TrimSpace(os.Getenv("MYWA_API_KEY")),
		DBPath: env("MYWA_DB_PATH", "mywa.db"),
	}
	c.MediaDir = env("MYWA_MEDIA_DIR", filepath.Join(filepath.Dir(c.DBPath), "media"))
	if err := c.LogLevel.UnmarshalText([]byte(env("MYWA_LOG_LEVEL", "info"))); err != nil {
		return c, errors.New("MYWA_LOG_LEVEL must be one of debug, info, warn, error")
	}
	if c.APIKey == "" {
		return c, errors.New("MYWA_API_KEY is required")
	}
	if len(c.APIKey) < 16 {
		return c, errors.New("MYWA_API_KEY must be at least 16 characters")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
