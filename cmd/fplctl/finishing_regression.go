// finishing-regression measures a genuinely different kind of signal than
// the captain-pick fixture-multiplier experiments in elo_backtest.go,
// minutes_backtest.go, and congestion_backtest.go: a buy/sell classifier,
// not a scoring-formula input. So it needs a different validation shape —
// a cohort comparison against real future output — rather than the
// substitution-into-blendFDR pattern those three share.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fantasypl/mcp/internal/insights"
	"github.com/fantasypl/mcp/internal/vaastav"
)

// minShotsOnTarget is the minimum xGOT-populated shots a player needs
// before their finishing-luck figure is trusted — below this, the sample
// is too small to say anything.
const minShotsOnTarget = 5

// finishingLuck is one player's shot-execution over/underperformance:
// actual goals from on-target shots minus the shot model's expected goals
// on target (xGOT) for those same shots.
//
// This isolates finishing luck from chance creation, which aggregate
// expected_goals conflates: a player can have poor aggregate xG because
// their chances are genuinely bad, because good chances are going off
// target, or because good on-target shots are being saved — only
// shot-level xG-vs-xGOT tells these apart, per Part B's plan. Positive
// Delta means outperforming shot quality (a "sell" signal — due to regress
// down); negative means underperforming it (a "buy" signal — due to
// regress up).
type finishingLuck struct {
	PlayerID      int
	Name          string
	ActualGoals   int
	SumXGOT       float64
	ShotsOnTarget int
}

func (f finishingLuck) Delta() float64 { return float64(f.ActualGoals) - f.SumXGOT }

// aggregateFinishingLuck fetches shots.csv for every gameweek in
// [fromGW, toGW] and aggregates each Premier League player's finishing luck
// across that span. Premier League shots are identified by match_id's
// "-prem-" segment — the FPL-Core-Insights match-id convention verified
// live — since shots.csv itself carries no competition column.
func aggregateFinishingLuck(ctx context.Context, ins *insights.Client, insSeason string, fromGW, toGW int) (map[int]*finishingLuck, error) {
	out := make(map[int]*finishingLuck)
	for gw := fromGW; gw <= toGW; gw++ {
		rows, err := ins.GameweekFile(ctx, insSeason, gw, "shots.csv")
		if errors.Is(err, insights.ErrNotAvailable) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("GW%d shots: %w", gw, err)
		}
		for _, row := range rows {
			if !strings.Contains(row["match_id"], "-prem-") {
				continue // European/cup shot — out of scope for this signal
			}
			if row["xgot"] == "" {
				continue // not on target, or xGOT genuinely unavailable for this shot
			}
			id := insights.Int(row["player_id"])
			if id == 0 {
				continue
			}
			fl, ok := out[id]
			if !ok {
				fl = &finishingLuck{PlayerID: id, Name: row["player_name"]}
				out[id] = fl
			}
			fl.SumXGOT += insights.Float(row["xgot"])
			fl.ShotsOnTarget++
			if row["outcome"] == "goal" {
				fl.ActualGoals++
			}
		}
	}
	return out, nil
}

// runFinishingRegression computes finishing luck through -split-gw, splits
// qualifying players into "buy" (most underperforming) and "sell" (most
// overperforming) groups of -group-size each, and compares their actual
// future FPL output (via vaastav.FuturePoints) over the rest of the season.
// If buy consistently outscores sell, the signal has real predictive value;
// if not, aggregate xG (which the algorithms already use) was good enough.
func runFinishingRegression(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("finishing-regression", flag.ExitOnError)
	season := fs.String("season", "2025-26", "vaastav season identifier, e.g. 2025-26")
	splitGW := fs.Int("split-gw", 20, "compute finishing luck through this gameweek; validate against gameweeks after it")
	toGW := fs.Int("to", 38, "last gameweek of the validation window")
	root := fs.String("root", ".", "project root; .cache/ lives under this")
	groupSize := fs.Int("group-size", 15, "how many players in each of the buy/sell comparison groups")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *splitGW < 2 || *splitGW >= *toGW {
		return fmt.Errorf("need 2 <= -split-gw < -to")
	}

	insSeason, err := insightsSeason(*season)
	if err != nil {
		return err
	}

	ins := insights.NewClient(filepath.Join(*root, ".cache", "insights"))
	luck, err := aggregateFinishingLuck(ctx, ins, insSeason, 1, *splitGW)
	if err != nil {
		return err
	}

	var qualifying []*finishingLuck
	for _, fl := range luck {
		if fl.ShotsOnTarget >= minShotsOnTarget {
			qualifying = append(qualifying, fl)
		}
	}
	sort.Slice(qualifying, func(i, j int) bool { return qualifying[i].Delta() < qualifying[j].Delta() })

	if len(qualifying) < 2**groupSize {
		return fmt.Errorf("only %d qualifying players (need >= %d shots on target each) — reduce -group-size or raise -split-gw",
			len(qualifying), minShotsOnTarget)
	}

	buy := qualifying[:*groupSize]                  // most underperforming vs shot quality
	sell := qualifying[len(qualifying)-*groupSize:] // most overperforming

	corpus := vaastav.NewCorpus(filepath.Join(*root, ".cache", "vaastav"))
	future, err := corpus.FuturePoints(ctx, *season, *splitGW+1, *toGW)
	if err != nil {
		return err
	}

	fmt.Printf("Finishing luck computed from GW1-%d (Premier League shots only), validated against GW%d-%d actual points.\n\n",
		*splitGW, *splitGW+1, *toGW)

	printGroup := func(label string, group []*finishingLuck) (totalPts, totalApps int) {
		fmt.Printf("-- %s --\n", label)
		fmt.Printf("%-22s %6s %8s %8s | %8s %8s %8s\n", "Player", "Goals", "xGOT", "Delta", "FutPts", "FutApps", "Pts/App")
		for _, fl := range group {
			fp := future[fl.PlayerID]
			fmt.Printf("%-22s %6d %8.2f %8.2f | %8d %8d %8.2f\n",
				frTruncate(fl.Name, 22), fl.ActualGoals, fl.SumXGOT, fl.Delta(), fp.Points, fp.Appearances, frPtsPerApp(fp))
			totalPts += fp.Points
			totalApps += fp.Appearances
		}
		fmt.Println()
		return totalPts, totalApps
	}

	buyPts, buyApps := printGroup(fmt.Sprintf("BUY signal (most underperforming shot quality, n=%d)", len(buy)), buy)
	sellPts, sellApps := printGroup(fmt.Sprintf("SELL signal (most overperforming shot quality, n=%d)", len(sell)), sell)

	fmt.Printf("Buy group:  %d total future points, %d appearances, %.2f pts/appearance\n", buyPts, buyApps, frSafeDiv(buyPts, buyApps))
	fmt.Printf("Sell group: %d total future points, %d appearances, %.2f pts/appearance\n", sellPts, sellApps, frSafeDiv(sellPts, sellApps))
	return nil
}

func frPtsPerApp(fp vaastav.FuturePlayerPoints) float64 { return frSafeDiv(fp.Points, fp.Appearances) }

func frSafeDiv(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func frTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
