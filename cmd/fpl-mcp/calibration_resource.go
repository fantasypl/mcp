package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fpl://calibration serves the signal calibration table `fplctl calibrate`
// writes (issue #52): how well form, points per game, captain_score, xGI per
// 90, minutes share and fixture difficulty have ranked next-gameweek points
// this season, overall and by position, with sample sizes.
//
// The server never computes it: rebuilding every gameweek's state takes a
// minute or more of vaastav downloads, too slow for a resource read. It
// reads the file at FPL_MCP_CALIBRATION_FILE, or calibration.json in the
// fpl-mcp cache directory, which is where fplctl calibrate writes by
// default. Re-run fplctl calibrate after each gameweek to keep it current.

const calibrationURI = "fpl://calibration"

// calibrationPath is where the server looks for the calibration report.
func calibrationPath() string {
	if p := strings.TrimSpace(os.Getenv("FPL_MCP_CALIBRATION_FILE")); p != "" {
		return p
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "fpl-mcp", "calibration.json")
}

// addCalibrationResource registers fpl://calibration, read from path on
// every request so a fresh fplctl run is picked up without a restart.
func addCalibrationResource(s *mcp.Server, path string) {
	s.AddResource(&mcp.Resource{
		URI: calibrationURI, Name: "calibration", Title: "Signal calibration",
		Description: "How well each signal (form, points per game, captain_score, xGI per 90, minutes share, fixture difficulty) has ranked next-gameweek FPL points this season: Spearman rank correlation, overall and for DEF, MID and FWD, with sample sizes and 95% intervals. Produced by fplctl calibrate.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		text, err := calibrationText(path)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: calibrationURI, MIMEType: "application/json", Text: text}}}, nil
	})
}

// calibrationText is the report with "available": true added, or a short
// explanation when there is no report yet.
func calibrationText(path string) (string, error) {
	notAvailable := func(note string) (string, error) {
		b, err := json.Marshal(map[string]any{"available": false, "note": note})
		return string(b), err
	}
	if path == "" {
		return notAvailable("No cache directory, so there is nowhere to read a calibration report from. Set FPL_MCP_CALIBRATION_FILE to a report written by fplctl calibrate.")
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return notAvailable("No calibration report yet. Run `fplctl calibrate` (after each gameweek, to keep it current); it writes " + path + ".")
	}
	if err != nil {
		return "", err
	}
	var report map[string]any
	if err := json.Unmarshal(b, &report); err != nil {
		return notAvailable("The calibration report at " + path + " is not valid JSON. Run `fplctl calibrate` again.")
	}
	report["available"] = true
	out, err := json.Marshal(report)
	return string(out), err
}
