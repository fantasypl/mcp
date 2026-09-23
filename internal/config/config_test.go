package config

import (
	"testing"
	"time"
)

func TestLoadTrimsWhitespace(t *testing.T) {
	t.Setenv("FPL_LOG_LEVEL", " debug\n")
	t.Setenv("FPL_CACHE_TTL", " 10m ")
	t.Setenv("FPL_LIVE_CACHE_TTL", "\t45 ")

	cfg := Load()
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
	if cfg.DefaultTTL != 10*time.Minute {
		t.Errorf("DefaultTTL = %v, want 10m", cfg.DefaultTTL)
	}
	if cfg.LiveTTL != 45*time.Second {
		t.Errorf("LiveTTL = %v, want 45s", cfg.LiveTTL)
	}
}

func TestLoadBlankValueUsesFallback(t *testing.T) {
	t.Setenv("FPL_LOG_LEVEL", "   ")
	if got := Load().LogLevel; got != "info" {
		t.Errorf("LogLevel = %q, want info fallback", got)
	}
}
