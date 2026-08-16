package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ajitem/fpl-intelligence/internal/algo"
	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

// runBacktest runs the captain-pick algorithm against a reconstructed
// historical gameweek and reports how the picks actually performed — a
// predictive-validity check, distinct from the golden-file parity tests,
// which only verify the implementation against the established outputs.
//
// Absorbed from the former cmd/backtest-demo prototype. Input is produced by
// scripts/backtest_from_vaastav.py: a bootstrap reflecting genuine
// season-to-date state through gameweek N-1 (no look-ahead), the full
// fixture list, and gameweek N's actual per-player results — deliberately
// not the live-fetch approach, which scores past gameweeks against *today's*
// bootstrap and is look-ahead-biased as a result.
func runBacktest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("backtest", flag.ExitOnError)
	bootstrapPath := fs.String("bootstrap", "", "path to reconstructed bootstrap JSON (single mode)")
	fixturesPath := fs.String("fixtures", "", "path to fixtures JSON (single mode)")
	actualPath := fs.String("actual", "", "path to actual-results JSON (single mode)")
	gw := fs.Int("gw", 0, "gameweek to predict (single mode)")
	topN := fs.Int("top", 10, "how many captain picks to show")

	batchDir := fs.String("dir", "", "testdata/backtest root (batch mode)")
	fromGW := fs.Int("from", 0, "first gameweek to predict (batch mode)")
	toGW := fs.Int("to", 0, "last gameweek to predict (batch mode)")
	jsonOut := fs.String("json", "", "batch mode: write the summary as JSON to this path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *batchDir != "" {
		return runBacktestBatch(*batchDir, *fromGW, *toGW, *jsonOut)
	}
	if *bootstrapPath == "" || *fixturesPath == "" || *gw == 0 {
		return fmt.Errorf("usage: fplctl backtest -bootstrap FILE -fixtures FILE -gw N [-actual FILE] [-top N]\n" +
			"   or: fplctl backtest -dir testdata/backtest -from N -to M [-json FILE]")
	}
	return runBacktestSingle(*bootstrapPath, *fixturesPath, *actualPath, *gw, *topN)
}

type btFileClient struct {
	bootstrap *fpl.Bootstrap
	fixtures  []fpl.Fixture
}

func (c *btFileClient) Bootstrap(context.Context) (*fpl.Bootstrap, error) { return c.bootstrap, nil }
func (c *btFileClient) Fixtures(context.Context) ([]fpl.Fixture, error)   { return c.fixtures, nil }
func (c *btFileClient) TeamPicks(context.Context, int, int) (*fpl.TeamPicks, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}
func (c *btFileClient) LivePoints(context.Context, int) (*fpl.LiveResponse, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}
func (c *btFileClient) EventStatus(context.Context) (*fpl.EventStatusResponse, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}
func (c *btFileClient) TeamHistory(context.Context, int) (*fpl.TeamHistory, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}
func (c *btFileClient) LeagueStandings(context.Context, int) (*fpl.LeagueStandings, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}
func (c *btFileClient) ManagerTransfers(context.Context, int) ([]fpl.ManagerTransfer, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}
func (c *btFileClient) PlayerSummary(context.Context, int) (*fpl.PlayerSummary, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}

type btActualEntry struct {
	WebName  string `json:"web_name"`
	Team     string `json:"team"`
	Position string `json:"position"`
	Points   int    `json:"points"`
	Minutes  int    `json:"minutes"`
}

func btLoadJSON[T any](path string) (T, error) {
	var v T
	b, err := os.ReadFile(path)
	if err != nil {
		return v, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, fmt.Errorf("decode %s: %w", path, err)
	}
	return v, nil
}

func runBacktestSingle(bootstrapPath, fixturesPath, actualPath string, gw, topN int) error {
	bootstrap, err := btLoadJSON[*fpl.Bootstrap](bootstrapPath)
	if err != nil {
		return err
	}
	fixtures, err := btLoadJSON[[]fpl.Fixture](fixturesPath)
	if err != nil {
		return err
	}
	client := &btFileClient{bootstrap: bootstrap, fixtures: fixtures}
	engine := algo.NewEngine(client)

	picks, err := engine.CaptainPicks(context.Background(), &gw, topN)
	if err != nil {
		return fmt.Errorf("CaptainPicks: %w", err)
	}

	fmt.Printf("Captain picks for GW%d, predicted from state through GW%d:\n\n", gw, gw-1)
	fmt.Printf("%-4s %-24s %-6s %-6s %8s", "Rank", "Player", "Team", "Pos", "Score")
	if actualPath != "" {
		fmt.Printf(" | %6s %6s", "Actual", "Min")
	}
	fmt.Println()

	var actual map[string]btActualEntry
	if actualPath != "" {
		actual, err = btLoadJSON[map[string]btActualEntry](actualPath)
		if err != nil {
			return err
		}
	}

	for _, p := range picks.Picks {
		fmt.Printf("%-4d %-24s %-6s %-6s %8.3f",
			p.Rank, btTruncate(p.Player.Name, 24), p.Player.Team, p.Player.Position, p.Score)
		if actual != nil {
			if a, ok := actual[fmt.Sprint(p.Player.ID)]; ok {
				fmt.Printf(" | %6d %6d", a.Points, a.Minutes)
			} else {
				fmt.Printf(" | %6s %6s", "?", "?")
			}
		}
		fmt.Println()
	}
	if actual == nil {
		return nil
	}
	fmt.Println()
	btSummarizeOne(gw, picks, actual, true)
	return nil
}

// btGWResult is one gameweek's outcome, kept for the batch aggregate.
type btGWResult struct {
	GW           int `json:"gw"`
	AlgoPoints   int `json:"algo_points"`
	AlgoRank     int `json:"algo_rank"` // 1 = best scorer that GW, 0 = did not play
	NaivePoints  int `json:"naive_points"`
	BestPossible int `json:"best_possible"`
	FieldSize    int `json:"field_size"`
	Top5Best     int `json:"top5_best"` // best actual return among the algorithm's top 5 picks
}

func runBacktestBatch(dir string, from, to int, jsonOut string) error {
	if from == 0 || to == 0 || to < from {
		return fmt.Errorf("batch mode needs -from N -to M")
	}

	var results []btGWResult
	for gw := from; gw <= to; gw++ {
		gwDir := filepath.Join(dir, fmt.Sprintf("gw%d", gw))
		bootstrapPath := filepath.Join(gwDir, fmt.Sprintf("bootstrap_gw%d.json", gw-1))
		fixturesPath := filepath.Join(gwDir, "fixtures.json")
		actualPath := filepath.Join(gwDir, fmt.Sprintf("actual_gw%d.json", gw))

		if _, err := os.Stat(bootstrapPath); err != nil {
			fmt.Printf("skipping GW%d: %v\n", gw, err)
			continue
		}

		bootstrap, err := btLoadJSON[*fpl.Bootstrap](bootstrapPath)
		if err != nil {
			return err
		}
		fixtures, err := btLoadJSON[[]fpl.Fixture](fixturesPath)
		if err != nil {
			return err
		}
		client := &btFileClient{bootstrap: bootstrap, fixtures: fixtures}
		engine := algo.NewEngine(client)
		gwCopy := gw
		picks, err := engine.CaptainPicks(context.Background(), &gwCopy, 5)
		if err != nil {
			return fmt.Errorf("GW%d: %w", gw, err)
		}
		actual, err := btLoadJSON[map[string]btActualEntry](actualPath)
		if err != nil {
			return err
		}

		res := btSummarizeOne(gw, picks, actual, false)
		res.NaivePoints = btNaiveBaseline(bootstrap, actual)
		for _, p := range picks.Picks {
			if a, ok := actual[fmt.Sprint(p.Player.ID)]; ok && a.Points > res.Top5Best {
				res.Top5Best = a.Points
			}
		}
		results = append(results, res)
	}

	btPrintBatchSummary(results)

	if jsonOut != "" {
		b, err := json.MarshalIndent(results, "", "  ")
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
	}
	return nil
}

// btNaiveBaseline captains whoever has the highest cumulative total_points
// going into the gameweek — "just pick the best player alive," the simplest
// strategy that isn't picking at random, and a fair bar for the model to
// clear rather than an easy one.
func btNaiveBaseline(bootstrap *fpl.Bootstrap, actual map[string]btActualEntry) int {
	best := -1
	bestPoints := -1
	for _, p := range bootstrap.Elements {
		if p.TotalPoints > bestPoints {
			bestPoints = p.TotalPoints
			best = p.ID
		}
	}
	if a, ok := actual[fmt.Sprint(best)]; ok {
		return a.Points
	}
	return 0
}

func btSummarizeOne(gw int, picks *algo.CaptainResult, actual map[string]btActualEntry, verbose bool) btGWResult {
	type ranked struct {
		id     string
		points int
		name   string
	}
	var all []ranked
	for id, a := range actual {
		if a.Minutes > 0 {
			all = append(all, ranked{id, a.Points, a.WebName})
		}
	}
	// actual is a map, so range order is randomised by Go on every run. A
	// plain points-descending sort leaves ties (common — many players score
	// exactly 2 or 3 in a gameweek) in that random order, making the rank of
	// a tied player nondeterministic run to run. Breaking ties by id fixes
	// that; sort.SliceStable alone would not, since the *input* order is what
	// varies, not just the sort's tie-handling.
	sort.Slice(all, func(i, j int) bool {
		if all[i].points != all[j].points {
			return all[i].points > all[j].points
		}
		return all[i].id < all[j].id
	})

	topPickID := fmt.Sprint(picks.Picks[0].Player.ID)
	topPickPoints := actual[topPickID].Points
	topPickMinutes := actual[topPickID].Minutes
	rank := 0
	for i, r := range all {
		if r.id == topPickID {
			rank = i + 1
			break
		}
	}

	if verbose {
		fmt.Printf("Algorithm's #1 pick (%s) scored %d points in GW%d", picks.Picks[0].Player.Name, topPickPoints, gw)
		switch {
		case rank > 0:
			fmt.Printf(" — rank %d of %d players who played.\n", rank, len(all))
		case topPickMinutes == 0:
			fmt.Println(" — did not play (0 minutes; an unused substitute or squad exclusion the model had no way to see coming).")
		default:
			fmt.Println(" — no matching record in the actual results file.")
		}
		if len(all) > 0 {
			fmt.Printf("Actual top scorer that GW: %s with %d points.\n", all[0].name, all[0].points)
		}
		fmt.Printf("With captaincy's 2x multiplier: %d points from this pick.\n", topPickPoints*2)
	}

	best := 0
	if len(all) > 0 {
		best = all[0].points
	}
	return btGWResult{
		GW: gw, AlgoPoints: topPickPoints, AlgoRank: rank,
		BestPossible: best, FieldSize: len(all),
	}
}

func btPrintBatchSummary(results []btGWResult) {
	fmt.Println()
	fmt.Printf("%-5s %8s %8s %8s %8s %10s %8s\n", "GW", "Algo#1", "Top5Best", "Naive", "Rank", "Best", "Field")
	var sumAlgo, sumTop5, sumNaive, sumBest int
	for _, r := range results {
		rankStr := fmt.Sprint(r.AlgoRank)
		if r.AlgoRank == 0 {
			rankStr = "DNP"
		}
		fmt.Printf("%-5d %8d %8d %8d %8s %10d %8d\n",
			r.GW, r.AlgoPoints, r.Top5Best, r.NaivePoints, rankStr, r.BestPossible, r.FieldSize)
		sumAlgo += r.AlgoPoints
		sumTop5 += r.Top5Best
		sumNaive += r.NaivePoints
		sumBest += r.BestPossible
	}

	n := len(results)
	if n == 0 {
		fmt.Println("no gameweeks evaluated")
		return
	}

	fmt.Println()
	fmt.Printf("Over %d gameweeks:\n", n)
	fmt.Printf("  Algorithm's #1 pick:            %3d total points  (%.1f avg/GW, x2 captaincy = %d)\n",
		sumAlgo, float64(sumAlgo)/float64(n), sumAlgo*2)
	fmt.Printf("  Best of algorithm's top 5:       %3d total points  (%.1f avg/GW, x2 captaincy = %d)\n",
		sumTop5, float64(sumTop5)/float64(n), sumTop5*2)
	fmt.Printf("  Naive (highest cumulative pts):  %3d total points  (%.1f avg/GW, x2 = %d)\n",
		sumNaive, float64(sumNaive)/float64(n), sumNaive*2)
	fmt.Printf("  Actual best possible:            %3d total points  (%.1f avg/GW)\n",
		sumBest, float64(sumBest)/float64(n))

	dnp := 0
	top10 := 0
	for _, r := range results {
		if r.AlgoRank == 0 {
			dnp++
		} else if r.AlgoRank <= 10 {
			top10++
		}
	}
	fmt.Printf("  #1 pick finished in the actual top 10 scorers: %d/%d gameweeks\n", top10, n)
	fmt.Printf("  #1 pick did not play at all: %d/%d gameweeks\n", dnp, n)
}

func btTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
