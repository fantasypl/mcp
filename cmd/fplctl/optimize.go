package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/fantasypl/mcp/internal/algo"
	"github.com/fantasypl/mcp/internal/store"
)

// runOptimize forces a fresh search (ignoring any cache-freshness check) and
// reports the result — but additionally persists to
// data/optimized_weights.json using
// the exact schema algo.GetOptimizedWeights writes, since fplctl exists to
// produce that artifact rather than just preview it.
func runOptimize(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("optimize", flag.ExitOnError)
	window := fs.Int("window", algo.RollingWindow, "rolling window of gameweeks to search over")
	root := fs.String("root", ".", "project root; data/ lives under this")
	if err := fs.Parse(args); err != nil {
		return err
	}

	layout := store.Layout{Root: *root}

	fmt.Println("Rolling Weight Optimizer")
	fmt.Println(strings.Repeat("=", 60))

	snapshotCount, liveCount := 0, 0
	for gw := 1; gw <= 38; gw++ {
		if _, ok, err := layout.LoadSnapshot(gw); err != nil {
			return err
		} else if ok {
			snapshotCount++
		}
		if _, ok, err := layout.LoadLiveData(gw); err != nil {
			return err
		} else if ok {
			liveCount++
		}
	}
	fmt.Printf("GW snapshots available: %d\n", snapshotCount)
	fmt.Printf("Live data cached: %d\n\n", liveCount)

	if snapshotCount < 3 {
		fmt.Println("Need at least 3 GW snapshots to optimize.")
		fmt.Println("Run: fplctl snapshot --backfill")
		fmt.Println("Then capture snapshots before each future GW deadline.")
		return nil
	}

	weights, ok, err := algo.OptimizeWeights(layout, *window)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("Optimization failed — check data availability.")
		return nil
	}

	base := algo.OptimizerBaseWeights()

	fmt.Println("\nOptimized weights:")
	names := make([]string, 0, len(weights))
	for k := range weights {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v := weights[k]
		baseV, ok := base[k]
		if !ok {
			baseV = v
		}
		delta := ""
		if math.Abs(v-baseV) > 0.001 {
			pct := 0.0
			if baseV != 0 {
				pct = (v - baseV) / baseV * 100
			}
			delta = fmt.Sprintf(" (%+.0f%%)", pct)
		}
		fmt.Printf("  %-30s %8.3f%s\n", k, v, delta)
	}

	now := time.Now()
	cache := &store.OptimizedWeightsCache{
		Weights:          weights,
		OptimizedAtEpoch: float64(now.UnixNano()) / 1e9,
		BaseWeights:      base,
		RollingWindow:    *window,
	}
	if err := layout.SaveOptimizedWeightsCache(cache); err != nil {
		return err
	}
	fmt.Printf("\nSaved to %s\n", layout.OptimizedWeightsPath())
	return nil
}
