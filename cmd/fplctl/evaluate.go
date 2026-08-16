package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/algo"
	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

// evalCSVColumns is data/evaluation.csv's fixed column order — a
// compatibility boundary for the established evaluation CSV contract.
var evalCSVColumns = []string{
	"gw", "algo_version", "captain_name", "captain_pts", "captain_rank",
	"top3_hit", "top5_hit", "top10_hit", "haaland_pts", "haaland_rank",
	"most_owned_name", "most_owned_pts", "most_owned_rank",
	"diff_hits_top50", "diff_total_pts", "diff_avg_pts",
	"algo_total_pts", "haaland_total_pts", "timestamp",
}

type evalPick struct {
	Name       string `json:"name"`
	ActualPts  int    `json:"actual_pts"`
	ActualRank int    `json:"actual_rank"`
}

type evalDiff struct {
	Name      string  `json:"name"`
	Ownership float64 `json:"ownership"`
	ActualPts int     `json:"actual_pts"`
}

type evalActual struct {
	Name   string `json:"name"`
	Points int    `json:"points"`
}

// evalResult is one gameweek's accuracy-evaluation result. The first block
// of fields is exactly evalCSVColumns; the rest is JSON-only detail.
type evalResult struct {
	GW              int     `json:"gw"`
	AlgoVersion     string  `json:"algo_version"`
	CaptainName     string  `json:"captain_name"`
	CaptainPts      int     `json:"captain_pts"`
	CaptainRank     int     `json:"captain_rank"`
	Top3Hit         int     `json:"top3_hit"`
	Top5Hit         int     `json:"top5_hit"`
	Top10Hit        int     `json:"top10_hit"`
	HaalandPts      int     `json:"haaland_pts"`
	HaalandRank     int     `json:"haaland_rank"`
	MostOwnedName   string  `json:"most_owned_name"`
	MostOwnedPts    int     `json:"most_owned_pts"`
	MostOwnedRank   int     `json:"most_owned_rank"`
	DiffHitsTop50   int     `json:"diff_hits_top50"`
	DiffTotalPts    int     `json:"diff_total_pts"`
	DiffAvgPts      float64 `json:"diff_avg_pts"`
	AlgoTotalPts    int     `json:"algo_total_pts"`
	HaalandTotalPts int     `json:"haaland_total_pts"`
	Timestamp       string  `json:"timestamp"`

	Top5Picks        []evalPick   `json:"top5_picks"`
	TopDifferentials []evalDiff   `json:"top_differentials"`
	ActualTop5       []evalActual `json:"actual_top5"`
}

func runEvaluate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("evaluate", flag.ExitOnError)
	gwFlag := fs.Int("gw", 0, "evaluate a specific gameweek")
	all := fs.Bool("all", false, "evaluate every finished gameweek")
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

	var finishedGWs []int
	for _, e := range bootstrap.Events {
		if e.Finished {
			finishedGWs = append(finishedGWs, e.ID)
		}
	}
	if len(finishedGWs) == 0 {
		fmt.Println("No finished gameweeks to evaluate.")
		return nil
	}

	var gwsToEval []int
	switch {
	case *gwFlag != 0:
		gwsToEval = []int{*gwFlag}
	case *all:
		gwsToEval = finishedGWs
	default:
		gwsToEval = []int{finishedGWs[len(finishedGWs)-1]}
	}

	evalCSVPath := filepath.Join(*root, "data", "evaluation.csv")
	evalJSONPath := filepath.Join(*root, "data", "evaluation.json")
	cacheDir := filepath.Join(*root, "data", "eval_cache")

	existingGWs, err := readEvaluatedGWs(evalCSVPath)
	if err != nil {
		return err
	}

	isFinished := make(map[int]bool, len(finishedGWs))
	for _, gw := range finishedGWs {
		isFinished[gw] = true
	}

	// btFileClient wraps the already-fetched bootstrap/fixtures so engine
	// calls below don't refetch per gameweek.
	engineClient := &btFileClient{bootstrap: bootstrap, fixtures: fixtures}
	engine := algo.NewEngine(engineClient)

	var allResults []evalResult
	var newGWs []int
	for _, gw := range gwsToEval {
		if !isFinished[gw] {
			fmt.Printf("  GW%d not finished yet — skipping\n", gw)
			continue
		}

		live, err := cachedLivePoints(ctx, client, cacheDir, gw)
		if err != nil {
			return err
		}

		result, err := evaluateGameweek(ctx, engine, bootstrap, gw, live)
		if err != nil {
			return err
		}
		allResults = append(allResults, result)

		if !existingGWs[gw] {
			if err := appendEvalCSV(evalCSVPath, result); err != nil {
				return err
			}
			newGWs = append(newGWs, gw)
		}

		printEvalResult(result)
	}

	if len(allResults) > 1 {
		printSeasonSummary(allResults)
	}

	if err := os.MkdirAll(filepath.Dir(evalJSONPath), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(allResults, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(evalJSONPath, b, 0o644); err != nil {
		return err
	}

	if len(newGWs) > 0 {
		fmt.Printf("  New evaluations saved to %s: GW%v\n", evalCSVPath, newGWs)
	}
	fmt.Printf("  Detailed results: %s\n", evalJSONPath)
	return nil
}

func cachedLivePoints(ctx context.Context, client *fpl.Client, cacheDir string, gw int) (*fpl.LiveResponse, error) {
	path := filepath.Join(cacheDir, fmt.Sprintf("live_gw%d.json", gw))
	if b, err := os.ReadFile(path); err == nil {
		var live fpl.LiveResponse
		if err := json.Unmarshal(b, &live); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		return &live, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	live, err := client.LivePoints(ctx, gw)
	if err != nil {
		return nil, fmt.Errorf("fetch live points GW%d: %w", gw, err)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	b, err := json.Marshal(live)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return nil, err
	}
	return live, nil
}

type rankedPoints struct {
	id     int
	name   string
	points int
	rank   int
}

func evaluateGameweek(ctx context.Context, engine *algo.Engine, bootstrap *fpl.Bootstrap, gw int, live *fpl.LiveResponse) (evalResult, error) {
	pointsByID := live.ActualPoints()

	playerPoints := make([]rankedPoints, 0, len(bootstrap.Elements))
	for _, p := range bootstrap.Elements {
		playerPoints = append(playerPoints, rankedPoints{id: p.ID, name: p.WebName, points: pointsByID[p.ID]})
	}
	// Stable descending sort — ties keep bootstrap.Elements order, preserving
	// deterministic ranking for equal scores.
	sort.SliceStable(playerPoints, func(i, j int) bool { return playerPoints[i].points > playerPoints[j].points })
	for i := range playerPoints {
		playerPoints[i].rank = i + 1
	}
	byID := make(map[int]rankedPoints, len(playerPoints))
	for _, pp := range playerPoints {
		byID[pp.id] = pp
	}

	// --- captain evaluation ---
	scored, err := engine.ScoreAllPlayers(ctx, &gw)
	if err != nil {
		return evalResult{}, err
	}
	if len(scored) == 0 {
		return evalResult{}, fmt.Errorf("no players scored for GW%d", gw)
	}
	topPick := scored[0].Player
	top5 := scored
	if len(top5) > 5 {
		top5 = top5[:5]
	}

	captainInfo := byID[topPick.ID]
	captainPts, captainRank := captainInfo.points, captainInfo.rank

	// --- baselines ---
	var haaland *fpl.Player
	for i := range bootstrap.Elements {
		if bootstrap.Elements[i].WebName == "Haaland" {
			haaland = &bootstrap.Elements[i]
			break
		}
	}
	haalandPts, haalandRank := 0, 999
	if haaland != nil {
		info := byID[haaland.ID]
		haalandPts, haalandRank = info.points, info.rank
	}

	mostOwned := bootstrap.Elements[0]
	for _, p := range bootstrap.Elements[1:] {
		if p.SelectedByPercent.Float() > mostOwned.SelectedByPercent.Float() {
			mostOwned = p
		}
	}
	moInfo := byID[mostOwned.ID]

	// --- differentials: this evaluation's own inline filter, narrower than
	// Differentials()'s InjuryStatuses — only "i"/"u", and a hardcoded 10%
	// cap rather than a parameter.
	type diffScored struct {
		score float64
		p     *fpl.Player
	}
	var diffs []diffScored
	for _, sp := range scored {
		p := sp.Player
		ownership := p.SelectedByPercent.Float()
		if ownership > 10.0 {
			continue
		}
		if p.Status == "i" || p.Status == "u" {
			continue
		}
		diffs = append(diffs, diffScored{algo.DifferentialScore(p, sp.Fixtures, ownership), p})
	}
	sort.SliceStable(diffs, func(i, j int) bool { return diffs[i].score > diffs[j].score })
	if len(diffs) > 10 {
		diffs = diffs[:10]
	}

	diffHitsTop50, diffTotalPts, diffCount := 0, 0, 0
	for _, d := range diffs {
		info := byID[d.p.ID]
		diffTotalPts += info.points
		diffCount++
		if info.rank <= 50 {
			diffHitsTop50++
		}
	}
	diffAvgPts := algo.Round(float64(diffTotalPts)/float64(max(1, diffCount)), 1)

	top5Picks := make([]evalPick, 0, len(top5))
	for _, sp := range top5 {
		info := byID[sp.Player.ID]
		top5Picks = append(top5Picks, evalPick{Name: sp.Player.WebName, ActualPts: info.points, ActualRank: info.rank})
	}

	topDiffsForJSON := diffs
	if len(topDiffsForJSON) > 5 {
		topDiffsForJSON = topDiffsForJSON[:5]
	}
	topDifferentials := make([]evalDiff, 0, len(topDiffsForJSON))
	for _, d := range topDiffsForJSON {
		topDifferentials = append(topDifferentials, evalDiff{
			Name: d.p.WebName, Ownership: d.p.SelectedByPercent.Float(), ActualPts: byID[d.p.ID].points,
		})
	}

	actualTop5 := make([]evalActual, 0, 5)
	for i := 0; i < len(playerPoints) && i < 5; i++ {
		actualTop5 = append(actualTop5, evalActual{Name: playerPoints[i].name, Points: playerPoints[i].points})
	}

	hit := func(rank, n int) int {
		if rank <= n {
			return 1
		}
		return 0
	}

	return evalResult{
		GW: gw, AlgoVersion: "2.5",
		CaptainName: topPick.WebName, CaptainPts: captainPts, CaptainRank: captainRank,
		Top3Hit: hit(captainRank, 3), Top5Hit: hit(captainRank, 5), Top10Hit: hit(captainRank, 10),
		HaalandPts: haalandPts, HaalandRank: haalandRank,
		MostOwnedName: mostOwned.WebName, MostOwnedPts: moInfo.points, MostOwnedRank: moInfo.rank,
		DiffHitsTop50: diffHitsTop50, DiffTotalPts: diffTotalPts, DiffAvgPts: diffAvgPts,
		AlgoTotalPts: captainPts, HaalandTotalPts: haalandPts,
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
		Top5Picks:        top5Picks,
		TopDifferentials: topDifferentials,
		ActualTop5:       actualTop5,
	}, nil
}

func readEvaluatedGWs(path string) (map[int]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[int]bool{}, nil
		}
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	out := map[int]bool{}
	if len(rows) == 0 {
		return out, nil
	}
	gwCol := -1
	for i, h := range rows[0] {
		if h == "gw" {
			gwCol = i
			break
		}
	}
	if gwCol == -1 {
		return out, nil
	}
	for _, row := range rows[1:] {
		if gwCol >= len(row) {
			continue
		}
		if n, err := strconv.Atoi(row[gwCol]); err == nil {
			out[n] = true
		}
	}
	return out, nil
}

func appendEvalCSV(path string, r evalResult) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	writeHeader := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeHeader = true
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if writeHeader {
		if err := w.Write(evalCSVColumns); err != nil {
			return err
		}
	}
	row := []string{
		strconv.Itoa(r.GW), r.AlgoVersion, r.CaptainName, strconv.Itoa(r.CaptainPts), strconv.Itoa(r.CaptainRank),
		strconv.Itoa(r.Top3Hit), strconv.Itoa(r.Top5Hit), strconv.Itoa(r.Top10Hit),
		strconv.Itoa(r.HaalandPts), strconv.Itoa(r.HaalandRank),
		r.MostOwnedName, strconv.Itoa(r.MostOwnedPts), strconv.Itoa(r.MostOwnedRank),
		strconv.Itoa(r.DiffHitsTop50), strconv.Itoa(r.DiffTotalPts), algo.FloatStr(r.DiffAvgPts),
		strconv.Itoa(r.AlgoTotalPts), strconv.Itoa(r.HaalandTotalPts), r.Timestamp,
	}
	return w.Write(row)
}

func printEvalResult(r evalResult) {
	fmt.Printf("\nGW%d Evaluation — Algorithm v%s\n", r.GW, r.AlgoVersion)
	fmt.Println("  CAPTAIN PICK")
	fmt.Printf("    Our pick:       %s -> %d pts (rank #%d)\n", r.CaptainName, r.CaptainPts, r.CaptainRank)
	fmt.Printf("    Haaland:        %d pts (rank #%d)\n", r.HaalandPts, r.HaalandRank)
	fmt.Printf("    Most owned:     %s -> %d pts (rank #%d)\n", r.MostOwnedName, r.MostOwnedPts, r.MostOwnedRank)
	fmt.Printf("    Top 3 hit: %v  Top 5: %v  Top 10: %v\n", r.Top3Hit == 1, r.Top5Hit == 1, r.Top10Hit == 1)

	fmt.Println("  DIFFERENTIALS")
	fmt.Printf("    Hits in top 50:    %d/10\n", r.DiffHitsTop50)
	fmt.Printf("    Avg pts:           %v\n", r.DiffAvgPts)
}

func printSeasonSummary(results []evalResult) {
	n := len(results)
	var totalCaptain, totalHaaland, totalMostOwned, sumTop3, sumTop5, sumTop10, sumRank, sumDiffHits int
	for _, r := range results {
		totalCaptain += r.CaptainPts
		totalHaaland += r.HaalandPts
		totalMostOwned += r.MostOwnedPts
		sumTop3 += r.Top3Hit
		sumTop5 += r.Top5Hit
		sumTop10 += r.Top10Hit
		sumRank += r.CaptainRank
		sumDiffHits += r.DiffHitsTop50
	}

	fmt.Printf("\nSEASON SUMMARY — %d gameweeks evaluated\n", n)
	fmt.Println("  CAPTAIN ACCURACY")
	fmt.Printf("    Top 3 hit rate:    %.1f%%\n", float64(sumTop3)/float64(n)*100)
	fmt.Printf("    Top 5 hit rate:    %.1f%%\n", float64(sumTop5)/float64(n)*100)
	fmt.Printf("    Top 10 hit rate:   %.1f%%\n", float64(sumTop10)/float64(n)*100)
	fmt.Printf("    Avg rank:          %.1f\n", float64(sumRank)/float64(n))
	fmt.Println("  TOTAL CAPTAIN POINTS")
	fmt.Printf("    Algorithm:         %d pts (%.1f avg)\n", totalCaptain, float64(totalCaptain)/float64(n))
	fmt.Printf("    Haaland:           %d pts (%.1f avg)\n", totalHaaland, float64(totalHaaland)/float64(n))
	fmt.Printf("    Most owned:        %d pts (%.1f avg)\n", totalMostOwned, float64(totalMostOwned)/float64(n))
	fmt.Printf("    vs Haaland:        %+d pts\n", totalCaptain-totalHaaland)
	fmt.Printf("    vs Most owned:     %+d pts\n", totalCaptain-totalMostOwned)
	fmt.Println("  DIFFERENTIALS")
	fmt.Printf("    Avg top-50 hits:   %.1f/10 per GW\n", float64(sumDiffHits)/float64(n))
}
