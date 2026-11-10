package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	// Aliased: odds_backtest.go already declares a function named calibration.
	signalcal "github.com/fantasypl/mcp/internal/calibration"
)

// runCalibrate writes the signal calibration table the server serves as
// fpl://calibration (issue #52): for each gameweek in the range, every
// player's state is rebuilt as it stood before the deadline from the vaastav
// corpus, the same reconstruction `fplctl backtest -seasons` uses, and each
// signal is correlated with the points scored that gameweek.
//
// Run it after each gameweek. With no -to it stops at the first gameweek the
// corpus has no results for, which is the one still to be played.
func runCalibrate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	season := fs.String("season", "2026-27", "vaastav season")
	from := fs.Int("from", 2, "first gameweek to measure (GW1 has no prior state)")
	to := fs.Int("to", 0, "last gameweek to measure; 0 means the last one with results")
	root := fs.String("root", ".", "project root; .cache/vaastav lives under this")
	out := fs.String("out", "", "output path; defaults to calibration.json in the fpl-mcp cache directory, where fpl-mcp reads it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from < 2 {
		return fmt.Errorf("-from must be at least 2: GW1 has no earlier state to measure from")
	}
	path := *out
	if path == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return fmt.Errorf("no cache directory; pass -out: %w", err)
		}
		path = filepath.Join(dir, "fpl-mcp", "calibration.json")
	}

	corpus := newVaastavCorpus(filepath.Join(*root, ".cache", "vaastav"))
	last := *to
	if last == 0 {
		last = 38
	}

	var samples []signalcal.Sample
	measured := 0
	for gw := *from; gw <= last; gw++ {
		c, err := corpus.BuildCase(ctx, *season, gw)
		if err != nil || len(c.Actual) == 0 {
			if *to == 0 && gw > *from {
				break // the first gameweek without results: stop here
			}
			return fmt.Errorf("GW%d: no results in the corpus: %v", gw, err)
		}
		s, err := signalcal.SamplesFromCase(ctx, c)
		if err != nil {
			return fmt.Errorf("GW%d: %w", gw, err)
		}
		samples = append(samples, s...)
		measured = gw
		fmt.Fprintf(os.Stderr, "GW%d: %d player-weeks\n", gw, len(s))
	}
	if measured == 0 {
		return fmt.Errorf("no gameweeks measured")
	}

	report := signalcal.Compute(samples, signalcal.DefaultSignals)
	report.Season, report.FromGW, report.ToGW = *season, *from, measured
	report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)

	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote %s: %s GW%d-GW%d, %d player-weeks.\n", path, *season, *from, measured, len(samples))
	for _, s := range report.Signals {
		rho := "n/a"
		if s.Overall.Rho != nil {
			rho = fmt.Sprintf("%+.3f [%+.3f, %+.3f]", *s.Overall.Rho, s.Overall.CI95[0], s.Overall.CI95[1])
		}
		fmt.Printf("  %-19s %s  n=%d\n", s.Name, rho, s.Overall.N)
	}
	return nil
}
