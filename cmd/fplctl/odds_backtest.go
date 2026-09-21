// odds-backtest measures whether bookmaker-implied clean-sheet probability
// (internal/marketodds, fed by football-data.co.uk's free odds history)
// predicts real clean sheets better than what FPL itself offers: the fixture
// difficulty rating (FDR).
//
// The comparison is on a clean-sheet probability forecast per team per match,
// scored by Brier score and log loss, with calibration deciles so a
// systematic bias is visible rather than averaged away. The FDR baseline is
// fitted leave-one-season-out — its per-(venue, difficulty) clean-sheet rate
// comes only from the *other* seasons — so it is not graded on data it saw.
// The market model has no fitted parameters at all, so it needs no such
// treatment.
//
// Like the other fplctl measurements, this is evidence for a decision, not
// the decision: a good result justifies surfacing market probabilities in
// tools; folding them into a ranking formula still needs its own tuning.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fantasypl/mcp/internal/footballdata"
	"github.com/fantasypl/mcp/internal/marketodds"
)

// csObs is one team-match: did the team keep a clean sheet, what did FPL's
// difficulty say, and what did the market say.
type csObs struct {
	season string
	home   bool
	fdr    int
	kept   bool
	market float64
}

func runOddsBacktest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("odds-backtest", flag.ExitOnError)
	seasonsFlag := fs.String("seasons", "2022-23,2023-24,2024-25,2025-26", "comma-separated seasons, e.g. 2024-25,2025-26")
	root := fs.String("root", ".", "project root; .cache/ lives under this")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fd := footballdata.NewClient(filepath.Join(*root, ".cache", "footballdata"))
	corpus := newVaastavCorpus(filepath.Join(*root, ".cache", "vaastav"))

	var obs []csObs
	for _, season := range strings.Split(*seasonsFlag, ",") {
		season = strings.TrimSpace(season)
		matches, err := fd.Season(ctx, season)
		if err != nil {
			return fmt.Errorf("%s odds: %w", season, err)
		}
		fixtures, names, err := corpus.SeasonFixtures(ctx, season)
		if err != nil {
			return fmt.Errorf("%s fixtures: %w", season, err)
		}
		type key struct{ h, a string }
		fdr := map[key][2]int{}
		for _, f := range fixtures {
			fdr[key{names[f.TeamH], names[f.TeamA]}] = [2]int{f.TeamHDifficulty, f.TeamADifficulty}
		}
		joined, skipped := 0, 0
		for _, m := range matches {
			if !m.Played {
				continue
			}
			d, ok := fdr[key{m.Home, m.Away}]
			model, err := marketodds.Model(m.Prices)
			if !ok || err != nil {
				skipped++
				continue
			}
			joined++
			obs = append(obs,
				csObs{season: season, home: true, fdr: d[0], kept: m.AwayGoals == 0, market: model.HomeCS},
				csObs{season: season, home: false, fdr: d[1], kept: m.HomeGoals == 0, market: model.AwayCS},
			)
		}
		fmt.Printf("%s: %d matches joined, %d skipped (no odds or unmatched team name)\n", season, joined, skipped)
	}
	if len(obs) == 0 {
		return fmt.Errorf("no observations")
	}

	// Leave-one-season-out FDR baseline: rate of clean sheets per
	// (venue, difficulty) computed from every other season.
	type cell struct {
		home bool
		fdr  int
	}
	rate := func(exclude string) (map[cell]float64, float64) {
		kept, n := map[cell]int{}, map[cell]int{}
		totalKept, total := 0, 0
		for _, o := range obs {
			if o.season == exclude {
				continue
			}
			c := cell{o.home, o.fdr}
			n[c]++
			total++
			if o.kept {
				kept[c]++
				totalKept++
			}
		}
		out := map[cell]float64{}
		for c, cnt := range n {
			out[c] = float64(kept[c]) / float64(cnt)
		}
		return out, float64(totalKept) / float64(total)
	}
	rates := map[string]map[cell]float64{}
	overall := map[string]float64{}
	for _, o := range obs {
		if _, ok := rates[o.season]; !ok {
			rates[o.season], overall[o.season] = rate(o.season)
		}
	}

	var fdrP, mktP, baseP []float64
	var outcome []bool
	for _, o := range obs {
		p, ok := rates[o.season][cell{o.home, o.fdr}]
		if !ok {
			p = overall[o.season]
		}
		fdrP = append(fdrP, p)
		mktP = append(mktP, o.market)
		baseP = append(baseP, overall[o.season])
		outcome = append(outcome, o.kept)
	}

	fmt.Printf("\n%d team-matches, actual clean-sheet rate %.1f%%\n\n", len(obs), 100*mean(boolsToFloat(outcome)))
	fmt.Printf("%-34s %8s %9s\n", "forecast", "Brier", "log loss")
	for _, r := range []struct {
		name string
		p    []float64
	}{
		{"league base rate", baseP},
		{"FPL FDR (leave-one-season-out)", fdrP},
		{"bookmaker-implied (no fitting)", mktP},
	} {
		fmt.Printf("%-34s %8.4f %9.4f\n", r.name, brier(r.p, outcome), logLoss(r.p, outcome))
	}
	fmt.Println("\nlower is better on both. Market calibration (predicted vs actual clean-sheet rate):")
	calibration(mktP, outcome)
	return nil
}

func boolsToFloat(b []bool) []float64 {
	out := make([]float64, len(b))
	for i, v := range b {
		if v {
			out[i] = 1
		}
	}
	return out
}

func mean(x []float64) float64 {
	s := 0.0
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}

func brier(p []float64, y []bool) float64 {
	s := 0.0
	for i := range p {
		t := 0.0
		if y[i] {
			t = 1
		}
		s += (p[i] - t) * (p[i] - t)
	}
	return s / float64(len(p))
}

func logLoss(p []float64, y []bool) float64 {
	const eps = 1e-6
	s := 0.0
	for i := range p {
		q := math.Min(1-eps, math.Max(eps, p[i]))
		if y[i] {
			s -= math.Log(q)
		} else {
			s -= math.Log(1 - q)
		}
	}
	return s / float64(len(p))
}

// calibration prints predicted vs actual rate across equal-count buckets.
func calibration(p []float64, y []bool) {
	idx := make([]int, len(p))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return p[idx[a]] < p[idx[b]] })
	const buckets = 5
	fmt.Printf("%-8s %10s %10s %6s\n", "bucket", "predicted", "actual", "n")
	for b := 0; b < buckets; b++ {
		lo, hi := b*len(idx)/buckets, (b+1)*len(idx)/buckets
		var ps, hits float64
		for _, i := range idx[lo:hi] {
			ps += p[i]
			if y[i] {
				hits++
			}
		}
		n := float64(hi - lo)
		fmt.Printf("%-8d %9.1f%% %9.1f%% %6d\n", b+1, 100*ps/n, 100*hits/n, hi-lo)
	}
}
