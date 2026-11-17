// Package config loads mywa settings from environment variables.
package config

import (
	"errors"
	"log/slog"
	"os"
	"strings"
)

type Config struct {
	Addr     string     // MYWA_ADDR, default ":8080"
	APIKey   string     // MYWA_API_KEY, required
	DBPath   string     // MYWA_DB_PATH, default "mywa.db"
	LogLevel slog.Level // MYWA_LOG_LEVEL: debug|info|warn|error, default info
}

func Load() (Config, error) {
	c := Config{
		Addr:   env("MYWA_ADDR", ":8080"),
		APIKey: os.Getenv("MYWA_API_KEY"),
		DBPath: env("MYWA_DB_PATH", "mywa.db"),
	}
	if err := c.LogLevel.UnmarshalText([]byte(env("MYWA_LOG_LEVEL", "info"))); err != nil {
		return c, errors.New("MYWA_LOG_LEVEL must be one of debug, info, warn, error")
	}
	if strings.TrimSpace(c.APIKey) == "" {
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
