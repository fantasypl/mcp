// role-change measures another cohort-comparison signal in the same shape
// finishing-regression.go pioneered: not a captain-pick fixture-multiplier
// substitution (see elo_backtest.go, minutes_backtest.go,
// congestion_backtest.go), but a classifier validated against real future
// output via vaastav.Corpus.FuturePoints.
//
// The positional-drift computation itself lives in internal/insights
// (Client.AveragePositions, ComputePositionDrift) rather than here, so
// production code can consume the same primitive this harness validates —
// this file is the measurement harness, not the only caller.
package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/fantasypl/mcp/internal/insights"
	"github.com/fantasypl/mcp/internal/vaastav"
)

// runRoleChangeRegression computes each qualifying player's positional
// drift between an earlier baseline window and a more recent one (both
// ending by -split-gw), splits them into an "advanced" group (the largest
// positive DeltaX — players pushed into a more advanced role) and a
// "control" group (drift closest to zero), and compares their actual
// future FPL output (via vaastav.FuturePoints) over the rest of the
// season. If the advanced group consistently outscores control, the
// positioning signal predicts a role change before goals/assists/ICT catch
// up; if not, the box score was already good enough.
func runRoleChangeRegression(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("role-change", flag.ExitOnError)
	season := fs.String("season", "2025-26", "vaastav season identifier, e.g. 2025-26")
	baselineFrom := fs.Int("baseline-from", 1, "first gameweek of the baseline window")
	baselineTo := fs.Int("baseline-to", 10, "last gameweek of the baseline window")
	recentFrom := fs.Int("recent-from", 11, "first gameweek of the recent window")
	splitGW := fs.Int("split-gw", 20, "last gameweek of the recent window; validate against gameweeks after it")
	toGW := fs.Int("to", 38, "last gameweek of the validation window")
	root := fs.String("root", ".", "project root; .cache/ lives under this")
	groupSize := fs.Int("group-size", 15, "how many players in each of the advanced/control comparison groups")
	minMatches := fs.Int("min-matches", 3, "minimum Premier League appearances required in each of the baseline/recent windows")
	minAvgMinutes := fs.Float64("min-avg-minutes", 0, "minimum average minutes played per appearance in the recent window (0 = no floor); filters out cameo-dominated samples")
	excludeGK := fs.Bool("exclude-gk", true, "exclude goalkeepers from both cohorts — their average position is structurally near-static, which otherwise stacks the control group with reliable-scoring GKs rather than genuinely stable-role outfielders")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *baselineFrom < 1 || *baselineTo < *baselineFrom || *recentFrom <= *baselineTo || *splitGW < *recentFrom || *splitGW >= *toGW {
		return fmt.Errorf("need 1 <= -baseline-from <= -baseline-to < -recent-from <= -split-gw < -to")
	}

	insSeason, err := insightsSeason(*season)
	if err != nil {
		return err
	}

	ins := insights.NewClient(filepath.Join(*root, ".cache", "insights"))
	baseline, err := ins.AveragePositions(ctx, insSeason, *baselineFrom, *baselineTo)
	if err != nil {
		return fmt.Errorf("baseline window: %w", err)
	}
	recent, err := ins.AveragePositions(ctx, insSeason, *recentFrom, *splitGW)
	if err != nil {
		return fmt.Errorf("recent window: %w", err)
	}
	var recentMinutes map[int]insights.PlayerMinutes
	if *minAvgMinutes > 0 {
		recentMinutes, err = ins.PlayerMinutesInRange(ctx, insSeason, *recentFrom, *splitGW)
		if err != nil {
			return fmt.Errorf("recent-window minutes: %w", err)
		}
	}
	drift := insights.ComputePositionDrift(baseline, recent)

	var qualifying []insights.PositionDrift
	for _, d := range drift {
		if d.BaselineMatches < *minMatches || d.RecentMatches < *minMatches {
			continue
		}
		if *excludeGK && d.Position == "G" {
			continue
		}
		if *minAvgMinutes > 0 && recentMinutes[d.PlayerID].AvgMinutes() < *minAvgMinutes {
			continue
		}
		qualifying = append(qualifying, d)
	}
	sort.Slice(qualifying, func(i, j int) bool { return qualifying[i].DeltaX() > qualifying[j].DeltaX() })

	if len(qualifying) < 2**groupSize {
		return fmt.Errorf("only %d qualifying players — reduce -group-size or widen the windows", len(qualifying))
	}

	advanced := qualifying[:*groupSize] // largest positive drift — pushed into a more advanced role
	// Control: drift closest to zero, i.e. no meaningful positional change.
	control := make([]insights.PositionDrift, len(qualifying))
	copy(control, qualifying)
	sort.Slice(control, func(i, j int) bool {
		ai, aj := control[i].DeltaX(), control[j].DeltaX()
		if ai < 0 {
			ai = -ai
		}
		if aj < 0 {
			aj = -aj
		}
		return ai < aj
	})
	control = control[:*groupSize]

	corpus := vaastav.NewCorpus(filepath.Join(*root, ".cache", "vaastav"))
	future, err := corpus.FuturePoints(ctx, *season, *splitGW+1, *toGW)
	if err != nil {
		return err
	}

	fmt.Printf("Positional drift: baseline GW%d-%d vs recent GW%d-%d, validated against GW%d-%d actual points.\n",
		*baselineFrom, *baselineTo, *recentFrom, *splitGW, *splitGW+1, *toGW)
	fmt.Printf("Filters: min-matches=%d min-avg-minutes=%.0f exclude-gk=%v (%d qualifying players)\n\n",
		*minMatches, *minAvgMinutes, *excludeGK, len(qualifying))

	printGroup := func(label string, group []insights.PositionDrift) (totalPts, totalApps int) {
		fmt.Printf("-- %s --\n", label)
		fmt.Printf("%-22s %4s %8s %8s %8s | %8s %8s %8s\n", "Player", "Pos", "BaseX", "RecentX", "DeltaX", "FutPts", "FutApps", "Pts/App")
		for _, d := range group {
			fp := future[d.PlayerID]
			fmt.Printf("%-22s %4s %8.1f %8.1f %8.1f | %8d %8d %8.2f\n",
				rcTruncate(d.Name, 22), d.Position, d.BaselineX, d.RecentX, d.DeltaX(), fp.Points, fp.Appearances, rcPtsPerApp(fp))
			totalPts += fp.Points
			totalApps += fp.Appearances
		}
		fmt.Println()
		return totalPts, totalApps
	}

	advPts, advApps := printGroup(fmt.Sprintf("ADVANCED signal (largest positive drift, n=%d)", len(advanced)), advanced)
	ctrlPts, ctrlApps := printGroup(fmt.Sprintf("CONTROL (drift closest to zero, n=%d)", len(control)), control)

	fmt.Printf("Advanced group: %d total future points, %d appearances, %.2f pts/appearance\n", advPts, advApps, rcSafeDiv(advPts, advApps))
	fmt.Printf("Control group:  %d total future points, %d appearances, %.2f pts/appearance\n", ctrlPts, ctrlApps, rcSafeDiv(ctrlPts, ctrlApps))
	return nil
}

func rcPtsPerApp(fp vaastav.FuturePlayerPoints) float64 { return rcSafeDiv(fp.Points, fp.Appearances) }

func rcSafeDiv(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func rcTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
