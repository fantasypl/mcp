package algo

import (
	"slices"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
	"github.com/ajitem/fpl-intelligence/internal/store"
)

// The rolling weight optimizer answers a narrow question: over the last few
// finished gameweeks, which weight set would have produced the best captain
// picks? It replays each gameweek's pre-deadline snapshot, scores every
// player with a trial weight set, and checks how many points the top pick
// actually returned. A coordinate descent over the eight weights that matter
// most, followed by a pairwise refinement of the biggest movers, searches
// that space in under two thousand trials instead of the ~590k a full grid
// would need.
//
// Important: the scoring function below is deliberately NOT captain.go's
// multiplicative v3.0 model. The optimizer predates that rewrite and still
// scores with the older additive form (base score plus a separate fixture
// term, rather than base score times a fixture multiplier), and it evaluates
// a smaller feature set — no set-piece, dream-team, defensive-contribution or
// news terms. That is a real divergence between what gets *tuned* and what
// gets *run*: an "optimized" weight set is only meaningful for the formula it
// was optimized against, and today those are two different formulas. This is
// preserved exactly as reference behaviour for the port; unifying the two
// scoring paths is exactly the kind of change that belongs in the backtested
// redesign pass, not here.

// optimizerBaseWeights are the weight optimizer's own starting point — see the
// package comment for why this differs from DefaultWeights().
func optimizerBaseWeights() map[string]float64 {
	return map[string]float64{
		"xg90":                       1.07,
		"xa90":                       0.92,
		"form":                       3.43,
		"ppg":                        5.92,
		"ep_next":                    0.49,
		"home":                       0.10,
		"fdr":                        0.30,
		"ict":                        0.01,
		"bonus_pg":                   1.31,
		"penalty":                    1.90,
		"set_piece":                  0.84,
		"dreamteam":                  0.56,
		"minutes_cert":               1.04,
		"def_contrib":                0.59,
		"playing_chance_max_penalty": -10.0,
	}
}

// tunableWeights are the only weights the search actually varies; the rest
// are considered too small or too binary in effect to be worth searching.
var tunableWeights = []string{"ppg", "form", "home", "fdr", "xg90", "xa90", "ep_next", "bonus_pg"}

// RollingWindow is how many recent finished gameweeks the optimizer considers.
const RollingWindow = 8

// WeightsCacheTTL is how long an optimized weight set is trusted before the
// search is re-run.
const WeightsCacheTTL = time.Hour

func cloneWeightMap(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// scorePlayerForOptimizer is the older, additive scoring formula the
// optimizer searches over. See the package comment for why this is not
// (*Engine).scorePlayer.
func scorePlayerForOptimizer(p *store.SnapshotPlayer, fixtures []TeamFixture, weights map[string]float64) float64 {
	get := func(key string, def float64) float64 {
		if v, ok := weights[key]; ok {
			return v
		}
		return def
	}

	form := p.Form.Float()
	ppg := p.PointsPerGame.Float()
	ict := p.ICTIndex.Float()

	nineties := 0.0
	if p.Minutes > 0 {
		nineties = float64(p.Minutes) / 90.0
	}
	xg90, xa90 := 0.0, 0.0
	if nineties > 0 {
		xg90 = p.ExpectedGoals.Float() / nineties
		xa90 = p.ExpectedAssists.Float() / nineties
	}

	bonusPG := float64(p.Bonus) / float64(max(1, p.Starts))

	penaltyNorm := 0.0
	if p.PenaltiesOrder != nil && *p.PenaltiesOrder == 1 {
		penaltyNorm = 1.0
	}

	epNext := p.EPNext.Float()

	gwPlayed := 1
	if nineties > 0 {
		gwPlayed = max(1, RoundToInt(nineties))
	}
	minutesCert := float64(p.Starts) / float64(max(1, gwPlayed))

	chancePenalty := 0.0
	if p.ChanceOfPlayingNextRound == nil {
		status := p.Status
		if status == "" {
			status = "a"
		}
		if InjuryStatuses[status] {
			chancePenalty = get("playing_chance_max_penalty", -10.0)
		}
	} else {
		chancePenalty = get("playing_chance_max_penalty", -10.0) * (1.0 - float64(*p.ChanceOfPlayingNextRound)/100.0)
	}

	baseScore := Normalize(xg90, 0, 1.0)*get("xg90", 1.5) +
		Normalize(xa90, 0, 0.5)*get("xa90", 1.2) +
		Normalize(form, 0, 10)*get("form", 2.8) +
		Normalize(ppg, 0, 10)*get("ppg", 3.5) +
		Normalize(epNext, 0, 10)*get("ep_next", 1.0) +
		Normalize(ict, 0, 300)*get("ict", 0.01) +
		Normalize(bonusPG, 0, 3)*get("bonus_pg", 1.1) +
		penaltyNorm*get("penalty", 1.5) +
		minutesCert*get("minutes_cert", 1.0) +
		chancePenalty

	if len(fixtures) == 0 {
		return baseScore + 0.5*get("fdr", 3.0)
	}

	fixtureScore := 0.0
	for _, f := range fixtures {
		fdrNorm := Normalize(5-f.FDR, 0, 4)
		homeBonus := 0.0
		if f.IsHome {
			homeBonus = get("home", 3.0)
		}
		fixtureScore += homeBonus + fdrNorm*get("fdr", 3.0)
	}
	return baseScore + fixtureScore
}

// buildRawFixtureMap is captain.buildFixtureMap without the team-strength
// blend: the optimizer searches over raw FDR, since blending in dynamic team
// strength would make a trial's score depend on data captured *after* the
// weight was supposedly chosen.
func buildRawFixtureMap(fixtures []fpl.Fixture, gameweek int) map[int][]TeamFixture {
	out := make(map[int][]TeamFixture)
	for i := range fixtures {
		f := &fixtures[i]
		if !f.InGameweek(gameweek) {
			continue
		}
		out[f.TeamH] = append(out[f.TeamH], TeamFixture{
			FDR: float64(f.TeamHDifficulty), IsHome: true, Opponent: f.TeamA,
		})
		out[f.TeamA] = append(out[f.TeamA], TeamFixture{
			FDR: float64(f.TeamADifficulty), IsHome: false, Opponent: f.TeamH,
		})
	}
	return out
}

// evaluateWeights scores every player in each gameweek's snapshot with
// weights, picks the top scorer as that gameweek's captain, and sums what
// they actually returned. A higher total means a better weight set.
func evaluateWeights(
	weights map[string]float64,
	gws []int,
	snapshots map[int]*store.Snapshot,
	liveData map[int]*store.LiveData,
	fixturesData []fpl.Fixture,
) int {
	total := 0

	for _, gw := range gws {
		snap := snapshots[gw]
		live := liveData[gw]
		if snap == nil || live == nil {
			continue
		}

		fixtureMap := buildRawFixtureMap(fixturesData, gw)
		actual := live.ActualPoints()

		bestScore := -999.0
		bestPlayerID := 0
		for i := range snap.Players {
			p := &snap.Players[i]
			score := scorePlayerForOptimizer(p, fixtureMap[p.Team], weights)
			if score > bestScore {
				bestScore = score
				bestPlayerID = p.ID
			}
		}

		if bestPlayerID != 0 {
			total += actual[bestPlayerID]
		}
	}

	return total
}

// coordinateDescentMultipliers is the search grid for phase 1: one weight
// varied at a time against the rest held fixed.
var coordinateDescentMultipliers = []float64{0.3, 0.5, 0.7, 1.0, 1.3, 1.5, 2.0}

// pairwiseMultipliers is the finer grid used in phase 2, once the search is
// down to the handful of weights that moved the most.
var pairwiseMultipliers = []float64{0.5, 0.75, 1.0, 1.25, 1.5}

// OptimizeWeights searches for the weight set that would have maximised
// captain-pick accuracy over the last maxGWs finished gameweeks with both a
// snapshot and live results on disk.
//
// Returns (nil, false, nil) — not an error — when there isn't enough data to
// optimize over: fewer than three qualifying gameweeks, or no fixtures cache
// at all. That mirrors the Python, which treats "insufficient data" as a
// routine, expected outcome rather than a failure.
func OptimizeWeights(layout store.Layout, maxGWs int) (map[string]float64, bool, error) {
	fixturesData, ok, err := layout.LoadFixturesCache()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}

	// Scan from the most recent gameweek backwards, keeping every snapshot
	// found along the way rather than discarding it and re-reading later —
	// the Python re-reads in a second pass, but there's no reason to pay disk
	// I/O twice for a file already in hand.
	var availableGWs []int
	snapshots := make(map[int]*store.Snapshot)
	liveData := make(map[int]*store.LiveData)
	for gw := 38; gw >= 1 && len(availableGWs) < maxGWs; gw-- {
		snap, snapOK, err := layout.LoadSnapshot(gw)
		if err != nil {
			return nil, false, err
		}
		live, liveOK, err := layout.LoadLiveData(gw)
		if err != nil {
			return nil, false, err
		}
		if snapOK && liveOK {
			availableGWs = append(availableGWs, gw)
			snapshots[gw] = snap
			liveData[gw] = live
		}
	}
	if len(availableGWs) < 3 {
		return nil, false, nil
	}

	base := optimizerBaseWeights()
	baseScore := evaluateWeights(base, availableGWs, snapshots, liveData, fixturesData)

	best := cloneWeightMap(base)
	bestScore := baseScore

	// Phase 1: coordinate descent, one weight at a time.
	for _, name := range tunableWeights {
		bestMult := 1.0
		for _, mult := range coordinateDescentMultipliers {
			trial := cloneWeightMap(best)
			trial[name] = base[name] * mult
			if score := evaluateWeights(trial, availableGWs, snapshots, liveData, fixturesData); score > bestScore {
				bestScore = score
				bestMult = mult
			}
		}
		best[name] = Round(base[name]*bestMult, 3)
	}

	// Phase 2: pairwise refinement of the four weights that moved the most.
	type mover struct {
		name   string
		change float64
	}
	movers := make([]mover, 0, len(tunableWeights))
	for _, name := range tunableWeights {
		change := (best[name] - base[name]) / max(0.001, base[name])
		if change < 0 {
			change = -change
		}
		movers = append(movers, mover{name, change})
	}
	slices.SortStableFunc(movers, func(a, b mover) int {
		switch {
		case a.change > b.change:
			return -1
		case a.change < b.change:
			return 1
		default:
			return 0
		}
	})
	if len(movers) > 4 {
		movers = movers[:4]
	}

	for i, m1 := range movers {
		for _, m2 := range movers[i+1:] {
			for _, mult1 := range pairwiseMultipliers {
				for _, mult2 := range pairwiseMultipliers {
					trial := cloneWeightMap(best)
					trial[m1.name] = Round(base[m1.name]*mult1, 3)
					trial[m2.name] = Round(base[m2.name]*mult2, 3)
					if score := evaluateWeights(trial, availableGWs, snapshots, liveData, fixturesData); score > bestScore {
						bestScore = score
						best[m1.name] = trial[m1.name]
						best[m2.name] = trial[m2.name]
					}
				}
			}
		}
	}

	return best, true, nil
}

// GetOptimizedWeights returns ready-to-use scoring weights, running a fresh
// optimization when the cache is stale or absent and persisting the result.
//
// The second return value reports whether an optimized set was actually
// applied — false means DefaultWeights() alone is in effect, either because
// there isn't yet enough season data to optimize over, or because the search
// found nothing that beat the hand-tuned baseline.
func GetOptimizedWeights(layout store.Layout, now time.Time) (Weights, bool, error) {
	if cache, ok, err := layout.LoadOptimizedWeightsCache(); err != nil {
		return Weights{}, false, err
	} else if ok && cache.Fresh(WeightsCacheTTL, now) {
		return MergeWeights(DefaultWeights(), cache.Weights), true, nil
	}

	optimized, ok, err := OptimizeWeights(layout, RollingWindow)
	if err != nil {
		return Weights{}, false, err
	}
	if !ok {
		return DefaultWeights(), false, nil
	}

	cache := &store.OptimizedWeightsCache{
		Weights:          optimized,
		OptimizedAtEpoch: float64(now.UnixNano()) / 1e9,
		BaseWeights:      optimizerBaseWeights(),
		RollingWindow:    RollingWindow,
	}
	if err := layout.SaveOptimizedWeightsCache(cache); err != nil {
		return Weights{}, false, err
	}

	return MergeWeights(DefaultWeights(), optimized), true, nil
}
