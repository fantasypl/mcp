// Command backtest-demo runs the captain-pick algorithm against a
// reconstructed historical gameweek and reports how the picks actually
// performed — a predictive-validity check, distinct from the golden-file
// parity tests, which only prove Go agrees with the Python reference.
//
// Input is produced by scripts/backtest_from_vaastav.py: a bootstrap
// reflecting genuine season-to-date state through gameweek N-1 (no
// look-ahead), the full fixture list, and gameweek N's actual per-player
// results. This program predicts gameweek N from the first two and scores
// the prediction against the third, either for a single gameweek or across a
// range with -batch.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/ajitem/fpl-intelligence/internal/algo"
	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

type fileClient struct {
	bootstrap *fpl.Bootstrap
	fixtures  []fpl.Fixture
}

func (c *fileClient) Bootstrap(context.Context) (*fpl.Bootstrap, error) { return c.bootstrap, nil }
func (c *fileClient) Fixtures(context.Context) ([]fpl.Fixture, error)   { return c.fixtures, nil }
func (c *fileClient) TeamPicks(context.Context, int, int) (*fpl.TeamPicks, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}
func (c *fileClient) LivePoints(context.Context, int) (*fpl.LiveResponse, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}
func (c *fileClient) EventStatus(context.Context) (*fpl.EventStatusResponse, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}
func (c *fileClient) TeamHistory(context.Context, int) (*fpl.TeamHistory, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}
func (c *fileClient) LeagueStandings(context.Context, int) (*fpl.LeagueStandings, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}
func (c *fileClient) ManagerTransfers(context.Context, int) ([]fpl.ManagerTransfer, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}
func (c *fileClient) PlayerSummary(context.Context, int) (*fpl.PlayerSummary, error) {
	return nil, fmt.Errorf("not needed for captain picks")
}

type actualEntry struct {
	WebName  string `json:"web_name"`
	Team     string `json:"team"`
	Position string `json:"position"`
	Points   int    `json:"points"`
	Minutes  int    `json:"minutes"`
}

func loadJSON[T any](path string) T {
	var v T
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, &v); err != nil {
		log.Fatalf("decode %s: %v", path, err)
	}
	return v
}

func main() {
	bootstrapPath := flag.String("bootstrap", "", "path to reconstructed bootstrap JSON (single mode)")
	fixturesPath := flag.String("fixtures", "", "path to fixtures JSON (single mode)")
	actualPath := flag.String("actual", "", "path to actual-results JSON (single mode)")
	gw := flag.Int("gw", 0, "gameweek to predict (single mode)")
	topN := flag.Int("top", 10, "how many captain picks to show")

	batchDir := flag.String("dir", "", "testdata/backtest root (batch mode)")
	fromGW := flag.Int("from", 0, "first gameweek to predict (batch mode)")
	toGW := flag.Int("to", 0, "last gameweek to predict (batch mode)")
	flag.Parse()

	if *batchDir != "" {
		runBatch(*batchDir, *fromGW, *toGW)
		return
	}
	if *bootstrapPath == "" || *fixturesPath == "" || *gw == 0 {
		log.Fatal("usage: backtest-demo -bootstrap FILE -fixtures FILE -gw N [-actual FILE] [-top N]\n" +
			"   or: backtest-demo -dir testdata/backtest -from N -to M")
	}
	runSingle(*bootstrapPath, *fixturesPath, *actualPath, *gw, *topN)
}

func runSingle(bootstrapPath, fixturesPath, actualPath string, gw, topN int) {
	client := &fileClient{
		bootstrap: loadJSON[*fpl.Bootstrap](bootstrapPath),
		fixtures:  loadJSON[[]fpl.Fixture](fixturesPath),
	}
	engine := algo.NewEngine(client)

	picks, err := engine.CaptainPicks(context.Background(), &gw, topN)
	if err != nil {
		log.Fatalf("CaptainPicks: %v", err)
	}

	fmt.Printf("Captain picks for GW%d, predicted from state through GW%d:\n\n", gw, gw-1)
	fmt.Printf("%-4s %-24s %-6s %-6s %8s", "Rank", "Player", "Team", "Pos", "Score")
	if actualPath != "" {
		fmt.Printf(" | %6s %6s", "Actual", "Min")
	}
	fmt.Println()

	var actual map[string]actualEntry
	if actualPath != "" {
		actual = loadJSON[map[string]actualEntry](actualPath)
	}

	for _, p := range picks.Picks {
		fmt.Printf("%-4d %-24s %-6s %-6s %8.3f",
			p.Rank, truncate(p.Player.Name, 24), p.Player.Team, p.Player.Position, p.Score)
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
		return
	}
	fmt.Println()
	summarizeOne(gw, client.bootstrap, picks, actual, true)
}

// gwResult is one gameweek's outcome, kept for the batch aggregate.
type gwResult struct {
	gw           int
	algoPoints   int
	algoRank     int // 1 = best scorer that GW, 0 = did not play
	naivePoints  int // baseline: captain whoever has the highest cumulative total_points so far
	bestPossible int
	fieldSize    int
	top5Best     int // best actual return among the algorithm's top 5 picks
}

func runBatch(dir string, from, to int) {
	if from == 0 || to == 0 || to < from {
		log.Fatal("batch mode needs -from N -to M")
	}

	var results []gwResult
	for gw := from; gw <= to; gw++ {
		gwDir := filepath.Join(dir, fmt.Sprintf("gw%d", gw))
		bootstrapPath := filepath.Join(gwDir, fmt.Sprintf("bootstrap_gw%d.json", gw-1))
		fixturesPath := filepath.Join(gwDir, "fixtures.json")
		actualPath := filepath.Join(gwDir, fmt.Sprintf("actual_gw%d.json", gw))

		if _, err := os.Stat(bootstrapPath); err != nil {
			log.Printf("skipping GW%d: %v", gw, err)
			continue
		}

		client := &fileClient{
			bootstrap: loadJSON[*fpl.Bootstrap](bootstrapPath),
			fixtures:  loadJSON[[]fpl.Fixture](fixturesPath),
		}
		engine := algo.NewEngine(client)
		gwCopy := gw
		picks, err := engine.CaptainPicks(context.Background(), &gwCopy, 5)
		if err != nil {
			log.Fatalf("GW%d: %v", gw, err)
		}
		actual := loadJSON[map[string]actualEntry](actualPath)

		res := summarizeOne(gw, client.bootstrap, picks, actual, false)
		res.naivePoints = naiveBaseline(client.bootstrap, actual)
		for _, p := range picks.Picks {
			if a, ok := actual[fmt.Sprint(p.Player.ID)]; ok && a.Points > res.top5Best {
				res.top5Best = a.Points
			}
		}
		results = append(results, res)
	}

	printBatchSummary(results)
}

// naiveBaseline captains whoever has the highest cumulative total_points
// going into the gameweek — "just pick the best player alive," the simplest
// strategy that isn't picking at random, and a fair bar for the model to
// clear rather than an easy one.
func naiveBaseline(bootstrap *fpl.Bootstrap, actual map[string]actualEntry) int {
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

func summarizeOne(gw int, bootstrap *fpl.Bootstrap, picks *algo.CaptainResult, actual map[string]actualEntry, verbose bool) gwResult {
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
	return gwResult{
		gw: gw, algoPoints: topPickPoints, algoRank: rank,
		bestPossible: best, fieldSize: len(all),
	}
}

func printBatchSummary(results []gwResult) {
	fmt.Println()
	fmt.Printf("%-5s %8s %8s %8s %8s %10s %8s\n", "GW", "Algo#1", "Top5Best", "Naive", "Rank", "Best", "Field")
	var sumAlgo, sumTop5, sumNaive, sumBest int
	for _, r := range results {
		rankStr := fmt.Sprint(r.algoRank)
		if r.algoRank == 0 {
			rankStr = "DNP"
		}
		fmt.Printf("%-5d %8d %8d %8d %8s %10d %8d\n",
			r.gw, r.algoPoints, r.top5Best, r.naivePoints, rankStr, r.bestPossible, r.fieldSize)
		sumAlgo += r.algoPoints
		sumTop5 += r.top5Best
		sumNaive += r.naivePoints
		sumBest += r.bestPossible
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
		if r.algoRank == 0 {
			dnp++
		} else if r.algoRank <= 10 {
			top10++
		}
	}
	fmt.Printf("  #1 pick finished in the actual top 10 scorers: %d/%d gameweeks\n", top10, n)
	fmt.Printf("  #1 pick did not play at all: %d/%d gameweeks\n", dnp, n)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
