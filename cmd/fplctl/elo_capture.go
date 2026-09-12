package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/fantasypl/mcp/internal/clubelo"
	"github.com/fantasypl/mcp/internal/fpl"
)

// eloCapture is what `fplctl elo-capture` writes: current Elo for every
// team in this season's bootstrap, plus each team's recent history — keyed
// by FPL team id (as a string, since JSON object keys must be strings) so a
// caller never needs clubelo's own slugs.
type eloCapture struct {
	CapturedAt string                      `json:"captured_at"`
	Current    map[string]float64          `json:"current"`
	History    map[string][]clubelo.Rating `json:"history"`
}

// runEloCapture archives clubelo.com's current-season team Elo ratings —
// the same data internal/clubelo already scrapes for the (rejected)
// fixture-multiplier experiment, reused here as a library rather than
// duplicated. Written under root/elo/teams.json so a companion repo like
// fantasypl/data can commit and version it, without that repo needing its
// own clubelo scraper.
func runEloCapture(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("elo-capture", flag.ExitOnError)
	root := fs.String("root", ".", "directory to write elo/teams.json under")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client := fpl.NewClient()
	bootstrap, err := client.Bootstrap(ctx)
	if err != nil {
		return fmt.Errorf("fetch bootstrap: %w", err)
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("resolve cache dir: %w", err)
	}
	elo := clubelo.NewClient(filepath.Join(cacheDir, "fplctl", "clubelo"))

	current, err := elo.CurrentByFPLTeam(ctx, bootstrap.Teams)
	if err != nil {
		// CurrentByFPLTeam returns whatever it found alongside the error —
		// still worth capturing rather than discarding over one unmatched
		// or unreachable team.
		fmt.Fprintf(os.Stderr, "elo-capture: %v\n", err)
	}
	currentByID := make(map[string]float64, len(current))
	for id, rating := range current {
		currentByID[strconv.Itoa(id)] = rating
	}

	history := make(map[string][]clubelo.Rating, len(bootstrap.Teams))
	for _, team := range bootstrap.Teams {
		slug, ok := clubelo.SlugFor(team.ShortName)
		if !ok {
			continue
		}
		hist, err := elo.History(ctx, slug)
		if err != nil {
			fmt.Fprintf(os.Stderr, "elo-capture: %s history: %v\n", team.Name, err)
			continue
		}
		history[strconv.Itoa(team.ID)] = hist
	}

	capture := eloCapture{
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		Current:    currentByID,
		History:    history,
	}
	b, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(*root, "elo", "teams.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote %s (%d current, %d with history)\n", path, len(currentByID), len(history))
	return nil
}
