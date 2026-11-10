package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// readCalibration serves fpl://calibration from path and returns its text.
func readCalibration(t *testing.T, path string) string {
	t.Helper()
	ctx := context.Background()
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	addCalibrationResource(s, path)
	t1, t2 := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "fpl://calibration"})
	if err != nil {
		t.Fatalf("read fpl://calibration: %v", err)
	}
	return res.Contents[0].Text
}

// Issue #52: fpl://calibration serves the report fplctl calibrate wrote.
func TestCalibrationResourceServesTheReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.json")
	report := `{"season":"2026-27","from_gameweek":2,"to_gameweek":10,"signals":[{"name":"form","overall":{"rho":0.21,"n":3000}}]}`
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readCalibration(t, path)
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if v["season"] != "2026-27" || v["available"] != true {
		t.Errorf("got %v, want the report with available: true", v)
	}
}

// Without a report the resource says so, and how to make one, rather than
// failing.
func TestCalibrationResourceWithoutAReport(t *testing.T) {
	got := readCalibration(t, filepath.Join(t.TempDir(), "missing.json"))
	if !strings.Contains(got, `"available":false`) || !strings.Contains(got, "fplctl calibrate") {
		t.Errorf("got %s, want available false and how to generate it", got)
	}
}
