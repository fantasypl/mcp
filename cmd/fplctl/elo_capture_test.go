package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fantasypl/mcp/internal/clubelo"
)

func TestWriteEloCaptureRefusesEmptyCapture(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "elo", "teams.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const previous = `{"captured_at":"2026-11-01T00:00:00Z","current":{"1":1900},"history":{}}`
	if err := os.WriteFile(path, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := writeEloCapture(root, eloCapture{
		CapturedAt: "2026-11-10T00:00:00Z",
		Current:    map[string]float64{},
		History:    map[string][]clubelo.Rating{},
	})
	if err == nil {
		t.Fatal("writeEloCapture accepted an empty capture")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != previous {
		t.Fatalf("previous capture was overwritten: %s", got)
	}
}

func TestWriteEloCaptureKeepsPartialCapture(t *testing.T) {
	root := t.TempDir()
	path, err := writeEloCapture(root, eloCapture{
		CapturedAt: "2026-11-10T00:00:00Z",
		Current:    map[string]float64{"1": 1900},
		History:    map[string][]clubelo.Rating{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("partial capture was not written: %v", err)
	}
}
