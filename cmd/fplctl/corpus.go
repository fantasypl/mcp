package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/fantasypl/mcp/internal/algo"
	"github.com/fantasypl/mcp/internal/vaastav"
)

// msResult is one gameweek's captain-pick outcome within a multi-season
// corpus backtest — the same shape as btGWResult, plus a season label so
// results from different seasons can be told apart once aggregated.
type msResult struct {
	Season       string `json:"season"`
	GW           int    `json:"gw"`
	AlgoPoints   int    `json:"algo_points"`
	AlgoRank     int    `json:"algo_rank"` // 1 = best scorer that GW, 0 = did not play
	NaivePoints  int    `json:"naive_points"`
	BestPossible int    `json:"best_possible"`
	FieldSize    int    `json:"field_size"`
	Top5Best     int    `json:"top5_best"`
}

// runBacktestCorpus runs the captain-pick algorithm across every gameweek in
// [from, to] for each of seasons, reconstructing point-in-time state on
// demand via internal/vaastav rather than from pre-generated testdata/
// fixtures — nothing here is committed to the repo, only cached locally
// under root/.cache/vaastav.
//
// If holdout names one of seasons, its results are aggregated and reported
// separately from the rest: the standard guard against overfitting when a
// model or its weights are tuned against these same seasons — see Part B's
// redesign-pass notes. A change should be judged by what it does to the
// held-out numbers, not the tuning-season numbers.
func runBacktestCorpus(ctx context.Context, root string, seasons []string, holdout string, from, to int, jsonOut string) error {
	if from == 0 || to == 0 || to < from {
		return fmt.Errorf("corpus mode needs -from N -to M")
	}
	if len(seasons) == 0 || (len(seasons) == 1 && seasons[0] == "") {
		return fmt.Errorf("corpus mode needs -seasons S1,S2,... (comma-separated, no trailing comma)")
	}
	if holdout != "" && !containsStr(seasons, holdout) {
		return fmt.Errorf("-holdout %q must be one of -seasons %v", holdout, seasons)
	}

	corpus := vaastav.NewCorpus(filepath.Join(root, ".cache", "vaastav"))

	var tuning, held []msResult
	for _, season := range seasons {
		fmt.Printf("== %s ==\n", season)
		for gw := from; gw <= to; gw++ {
			if gw < 2 {
				continue // no prior state to reconstruct for GW1
			}
			c, err := corpus.BuildCase(ctx, season, gw)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  skipping %s GW%d: %v\n", season, gw, err)
				continue
			}
			res, err := scoreCaptainPicks(ctx, c)
			if err != nil {
				return fmt.Errorf("%s GW%d: %w", season, gw, err)
			}
			if season == holdout {
				held = append(held, res)
			} else {
				tuning = append(tuning, res)
			}
		}
	}

	fmt.Println("\n--- Tuning seasons (in-sample) ---")
	printCorpusSummary(tuning)
	if holdout != "" {
		fmt.Printf("\n--- Held-out season: %s (out-of-sample) ---\n", holdout)
		printCorpusSummary(held)
	}

	if jsonOut == "" {
		return nil
	}
	out := struct {
		Tuning  []msResult `json:"tuning"`
		Holdout []msResult `json:"holdout,omitempty"`
	}{tuning, held}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(jsonOut), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(jsonOut, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("\nWrote %s\n", jsonOut)
	return nil
}

// scoreCaptainPicks runs the captain algorithm against c's reconstructed
// state and scores its #1 pick against c's actual results — the corpus-mode
// analogue of btSummarizeOne/btNaiveBaseline, adapted to vaastav.Case's
// int-keyed Actual map instead of the batch mode's JSON-decoded string keys.
func scoreCaptainPicks(ctx context.Context, c *vaastav.Case) (msResult, error) {
	client := &btFileClient{bootstrap: c.Bootstrap, fixtures: c.Fixtures}
	engine := algo.NewEngine(client)
	gw := c.PredictGW
	picks, err := engine.CaptainPicks(ctx, &gw, 5)
	if err != nil {
		return msResult{}, err
	}

	type ranked struct {
		id     int
		points int
	}
	var all []ranked
	for id, a := range c.Actual {
		if a.Minutes > 0 {
			all = append(all, ranked{id, a.Points})
		}
	}
	// c.Actual is a map, so range order is randomized run to run. Sorting by
	// points descending, id ascending on ties keeps rank deterministic.
	sort.Slice(all, func(i, j int) bool {
		if all[i].points != all[j].points {
			return all[i].points > all[j].points
		}
		return all[i].id < all[j].id
	})

	topID := picks.Picks[0].Player.ID
	topPoints := c.Actual[topID].Points
	rank := 0
	for i, r := range all {
		if r.id == topID {
			rank = i + 1
			break
		}
	}

	top5Best := 0
	for _, p := range picks.Picks {
		if a, ok := c.Actual[p.Player.ID]; ok && a.Points > top5Best {
			top5Best = a.Points
		}
	}

	// Naive baseline: captain whoever has the highest cumulative total_points
	// going into the gameweek — the simplest strategy that isn't picking at
	// random, and a fair bar for the model to clear rather than an easy one.
	naiveID, naiveBest := 0, -1
	for _, p := range c.Bootstrap.Elements {
		if p.TotalPoints > naiveBest {
			naiveBest = p.TotalPoints
			naiveID = p.ID
		}
	}
	naivePoints := c.Actual[naiveID].Points

	best := 0
	if len(all) > 0 {
		best = all[0].points
	}

	return msResult{
		Season: c.Season, GW: c.PredictGW,
		AlgoPoints: topPoints, AlgoRank: rank, NaivePoints: naivePoints,
		BestPossible: best, FieldSize: len(all), Top5Best: top5Best,
	}, nil
}

func printCorpusSummary(results []msResult) {
	if len(results) == 0 {
		fmt.Println("  no gameweeks evaluated")
		return
	}

	seasonsSeen := map[string]bool{}
	var sumAlgo, sumTop5, sumNaive, sumBest, dnp, top10 int
	for _, r := range results {
		seasonsSeen[r.Season] = true
		sumAlgo += r.AlgoPoints
		sumTop5 += r.Top5Best
		sumNaive += r.NaivePoints
		sumBest += r.BestPossible
		if r.AlgoRank == 0 {
			dnp++
		} else if r.AlgoRank <= 10 {
			top10++
		}
	}
	n := len(results)

	fmt.Printf("  %d gameweeks across %d season(s)\n", n, len(seasonsSeen))
	fmt.Printf("  Algorithm's #1 pick:            %6d total points  (%.2f avg/GW, x2 captaincy = %d)\n",
		sumAlgo, float64(sumAlgo)/float64(n), sumAlgo*2)
	fmt.Printf("  Best of algorithm's top 5:       %6d total points  (%.2f avg/GW, x2 captaincy = %d)\n",
		sumTop5, float64(sumTop5)/float64(n), sumTop5*2)
	fmt.Printf("  Naive (highest cumulative pts):  %6d total points  (%.2f avg/GW, x2 = %d)\n",
		sumNaive, float64(sumNaive)/float64(n), sumNaive*2)
	fmt.Printf("  Actual best possible:            %6d total points  (%.2f avg/GW)\n",
		sumBest, float64(sumBest)/float64(n))
	fmt.Printf("  #1 pick finished in the actual top 10 scorers: %d/%d gameweeks\n", top10, n)
	fmt.Printf("  #1 pick did not play at all: %d/%d gameweeks\n", dnp, n)
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
