package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
	"github.com/ajitem/fpl-intelligence/internal/store"
)

// runSnapshot ports scripts/snapshot_gw.py.
func runSnapshot(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	gw := fs.Int("gw", 0, "specific gameweek to snapshot (default: auto-detect next upcoming GW)")
	backfill := fs.Bool("backfill", false, "save current data as the latest finished GW")
	root := fs.String("root", ".", "project root; data/ lives under this")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client := fpl.NewClient()
	bootstrap, err := client.Bootstrap(ctx)
	if err != nil {
		return fmt.Errorf("fetch bootstrap: %w", err)
	}
	fixtures, err := client.Fixtures(ctx)
	if err != nil {
		return fmt.Errorf("fetch fixtures: %w", err)
	}

	targetGW, err := resolveTargetGW(bootstrap, *gw, *backfill)
	if err != nil {
		return err
	}

	layout := store.Layout{Root: *root}
	path := layout.SnapshotPath(targetGW)
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("Snapshot for GW%d already exists at %s\n", targetGW, path)
		fmt.Println("Use --gw N to overwrite a specific GW, or skip.")
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	snap := store.SnapshotFromBootstrap(bootstrap, fixtures, targetGW, *backfill, time.Now())
	if err := layout.SaveSnapshot(snap); err != nil {
		return err
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	fmt.Printf("Snapshot saved: GW%d -> %s (%.0f KB)\n", targetGW, path, float64(info.Size())/1024)
	fmt.Printf("  Players: %d\n", len(snap.Players))
	fmt.Printf("  Teams: %d\n", len(snap.Teams))
	fmt.Printf("  Captured at: %s\n", snap.CapturedAt)
	if *backfill {
		fmt.Printf("  (backfill — using current stats as GW%d snapshot)\n", targetGW)
	}
	return nil
}

// resolveTargetGW ports take_snapshot's target-GW selection:
//   - an explicit --gw wins outright
//   - --backfill picks the highest-ID finished gameweek
//   - otherwise, the lowest-ID gameweek that isn't finished yet
func resolveTargetGW(b *fpl.Bootstrap, explicitGW int, backfill bool) (int, error) {
	if explicitGW != 0 {
		return explicitGW, nil
	}
	if backfill {
		found, best := false, 0
		for _, e := range b.Events {
			if e.Finished && (!found || e.ID > best) {
				best, found = e.ID, true
			}
		}
		if !found {
			return 0, fmt.Errorf("no finished gameweeks found")
		}
		return best, nil
	}
	found, best := false, 0
	for _, e := range b.Events {
		if !e.Finished && (!found || e.ID < best) {
			best, found = e.ID, true
		}
	}
	if !found {
		return 0, fmt.Errorf("no upcoming gameweeks found")
	}
	return best, nil
}
