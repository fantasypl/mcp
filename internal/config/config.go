// Package config contains the small set of startup settings used by the MCP server.
package config

import (
	"os"
	"strconv"
	"time"
)

// Config is intentionally limited to FPL server settings.
type Config struct {
	LogLevel   string
	DefaultTTL time.Duration
	LiveTTL    time.Duration
	EntryTTL   time.Duration
	LeagueTTL  time.Duration
}

func Load() Config {
	return Config{
		LogLevel:   envString("FPL_LOG_LEVEL", "info"),
		DefaultTTL: envDuration("FPL_CACHE_TTL", 5*time.Minute),
		LiveTTL:    envDuration("FPL_LIVE_CACHE_TTL", 30*time.Second),
		EntryTTL:   envDuration("FPL_ENTRY_CACHE_TTL", time.Minute),
		LeagueTTL:  envDuration("FPL_LEAGUE_CACHE_TTL", 2*time.Minute),
	}
}

func envString(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	if duration, err := time.ParseDuration(value); err == nil && duration >= 0 {
		return duration
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}
