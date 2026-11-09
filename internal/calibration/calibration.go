// Package calibration measures which signals have predicted FPL points this
// season (issue #52).
//
// For each gameweek, it rebuilds every player's state as it stood before the
// deadline (vaastav.Corpus.BuildCase, the same point-in-time reconstruction
// fplctl's backtests use), reads each signal from that state, and pairs it
// with the points the player then scored. Spearman's rank correlation
// between a signal and those next-gameweek points says how well the signal
// ranked players, with 1 a perfect order and 0 no better than chance.
//
// Every gameweek's player-weeks are pooled into one sample per signal, so a
// signal that sorts players well week after week scores well. Each figure
// carries its sample size and a 95% confidence interval, so early-season
// noise is visible: after three gameweeks a correlation of 0.25 can easily be
// 0.15 or 0.35.
package calibration

import (
	"context"
	"math"
	"sort"

	"github.com/fantasypl/mcp/internal/algo"
	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/vaastav"
)

// Sample is one player-week: the signals as they stood before the deadline,
// and the points scored that gameweek.
type Sample struct {
	Gameweek   int
	PlayerID   int
	Position   string
	Signals    map[string]float64
	NextPoints int
}

// SignalDef names and describes one signal.
type SignalDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// DefaultSignals are the signals the issue asked about.
var DefaultSignals = []SignalDef{
	{"form", "FPL form: average points over the last 30 days, as reconstructed before the deadline."},
	{"points_per_game", "Season points per game to date."},
	{"captain_score", "This server's captain_score for the gameweek, with the default weights."},
	{"xgi_per_90", "Expected goal involvements per 90 minutes, season to date."},
	{"minutes_share", "Minutes played as a share of all minutes available so far (90 per gameweek)."},
	{"fixture_difficulty", "FPL's fixture difficulty (1-5) for the gameweek, averaged over a double. Higher is harder, so a useful signal correlates negatively."},
}

// Positions reported separately. Goalkeepers count in the overall figure.
var Positions = []string{"DEF", "MID", "FWD"}

// minMinutesToDate keeps the sample to players with at least one full
// match's worth of minutes, so per-90 signals are defined and season-long
// absentees don't inflate the correlations with a pile of zeros.
const minMinutesToDate = 90

// Stat is one correlation with its uncertainty.
type Stat struct {
	// Rho is nil when there are fewer than three samples or a signal never
	// varies.
	Rho *float64 `json:"rho"`
	N   int      `json:"n"`
	// CI95 is the 95% interval for rho from the Fisher transformation.
	CI95 [2]float64 `json:"ci95"`
	// Reliability is "high" from 1,000 samples, "medium" from 200, and
	// "low" below that.
	Reliability string `json:"reliability"`
}

// SignalResult is one signal's correlation overall and by position.
type SignalResult struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Overall     Stat            `json:"overall"`
	ByPosition  map[string]Stat `json:"by_position"`
}

// Report is the calibration table served as fpl://calibration.
type Report struct {
	Season      string         `json:"season"`
	FromGW      int            `json:"from_gameweek"`
	ToGW        int            `json:"to_gameweek"`
	Gameweeks   int            `json:"gameweeks"`
	GeneratedAt string         `json:"generated_at,omitempty"`
	Method      string         `json:"method"`
	Population  string         `json:"population"`
	Signals     []SignalResult `json:"signals"`
}

const method = "Spearman rank correlation between each signal, as it stood before the deadline, and the points the player scored that gameweek. Every gameweek's player-weeks are pooled. ci95 is the 95% interval from the Fisher transformation; reliability is high from 1,000 samples, medium from 200 and low below that. A positive rho means the signal ranked players in the order they scored; fixture_difficulty should be negative."

const population = "Players with at least 90 minutes before the gameweek and a fixture in it."

// Compute builds the report from pooled samples.
func Compute(samples []Sample, defs []SignalDef) Report {
	r := Report{Method: method, Population: population}
	gws := map[int]bool{}
	for _, s := range samples {
		gws[s.Gameweek] = true
	}
	r.Gameweeks = len(gws)

	for _, def := range defs {
		res := SignalResult{Name: def.Name, Description: def.Description, ByPosition: map[string]Stat{}}
		res.Overall = correlate(samples, def.Name, "")
		for _, pos := range Positions {
			res.ByPosition[pos] = correlate(samples, def.Name, pos)
		}
		r.Signals = append(r.Signals, res)
	}
	return r
}

// correlate computes one signal's Stat over the samples in position (all of
// them when position is "").
func correlate(samples []Sample, signal, position string) Stat {
	var xs, ys []float64
	for _, s := range samples {
		if position != "" && s.Position != position {
			continue
		}
		v, ok := s.Signals[signal]
		if !ok {
			continue
		}
		xs = append(xs, v)
		ys = append(ys, float64(s.NextPoints))
	}
	rho, ok := Spearman(xs, ys)
	if !ok {
		return Stat{N: len(xs), Reliability: "low"}
	}
	return statFor(rho, len(xs))
}

// statFor attaches the interval and reliability to a correlation.
func statFor(rho float64, n int) Stat {
	st := Stat{Rho: ptr(round3(rho)), N: n, Reliability: "low"}
	switch {
	case n >= 1000:
		st.Reliability = "high"
	case n >= 200:
		st.Reliability = "medium"
	}
	if n > 3 {
		// Fisher: atanh(rho) is roughly normal with sd 1/sqrt(n-3). Clamp
		// rho off +/-1, where atanh is infinite.
		z := math.Atanh(math.Max(-0.999999, math.Min(0.999999, rho)))
		half := 1.96 / math.Sqrt(float64(n-3))
		st.CI95 = [2]float64{round3(math.Tanh(z - half)), round3(math.Tanh(z + half))}
	} else {
		st.CI95 = [2]float64{-1, 1}
	}
	return st
}

// Spearman is the rank correlation of xs and ys, with tied values given
// their average rank. It reports false for fewer than three pairs or when
// either side never varies.
func Spearman(xs, ys []float64) (float64, bool) {
	if len(xs) != len(ys) || len(xs) < 3 {
		return 0, false
	}
	return pearson(ranks(xs), ranks(ys))
}

// ranks returns 1-based ranks, ties averaged.
func ranks(vs []float64) []float64 {
	idx := make([]int, len(vs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return vs[idx[a]] < vs[idx[b]] })
	out := make([]float64, len(vs))
	for i := 0; i < len(idx); {
		j := i
		for j+1 < len(idx) && vs[idx[j+1]] == vs[idx[i]] {
			j++
		}
		avg := float64(i+j)/2 + 1 // positions i..j share ranks i+1..j+1
		for k := i; k <= j; k++ {
			out[idx[k]] = avg
		}
		i = j + 1
	}
	return out
}

// pearson is the Pearson correlation, false when either side is constant.
func pearson(xs, ys []float64) (float64, bool) {
	n := float64(len(xs))
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx /= n
	my /= n
	var cov, vx, vy float64
	for i := range xs {
		dx, dy := xs[i]-mx, ys[i]-my
		cov += dx * dy
		vx += dx * dx
		vy += dy * dy
	}
	if vx == 0 || vy == 0 {
		return 0, false
	}
	return cov / math.Sqrt(vx*vy), true
}

// SamplesFromCase reads every signal for one reconstructed gameweek.
func SamplesFromCase(ctx context.Context, c *vaastav.Case) ([]Sample, error) {
	// captain_score comes from the real scoring code, run over the
	// reconstructed state. The stub client serves the case's own bootstrap
	// and fixtures, so nothing touches the network.
	engine := algo.NewEngine(algo.NewStubClient(c.Bootstrap, c.Fixtures))
	gw := c.PredictGW
	scored, err := engine.ScoreAllPlayers(ctx, &gw)
	if err != nil {
		return nil, err
	}

	difficulty := fixtureDifficulty(c.Fixtures, gw)
	availableMinutes := float64(90 * max(1, gw-1))

	samples := make([]Sample, 0, len(scored))
	for _, sp := range scored {
		p := sp.Player
		fdr, playing := difficulty[p.Team]
		if !playing || p.Minutes < minMinutesToDate {
			continue
		}
		actual, ok := c.Actual[p.ID]
		if !ok {
			continue
		}
		xgi90 := p.ExpectedGoalInvolvements.Float() / (float64(p.Minutes) / 90)
		samples = append(samples, Sample{
			Gameweek: gw, PlayerID: p.ID, Position: algo.Position(p.ElementType), NextPoints: actual.Points,
			Signals: map[string]float64{
				"form":               p.Form.Float(),
				"points_per_game":    p.PointsPerGame.Float(),
				"captain_score":      sp.Score,
				"xgi_per_90":         xgi90,
				"minutes_share":      math.Min(1, float64(p.Minutes)/availableMinutes),
				"fixture_difficulty": fdr,
			},
		})
	}
	return samples, nil
}

// fixtureDifficulty is each club's average FPL difficulty in gameweek gw;
// a club missing from the map has no fixture.
func fixtureDifficulty(fixtures []fpl.Fixture, gw int) map[int]float64 {
	sum := map[int]float64{}
	count := map[int]int{}
	for _, f := range fixtures {
		if !f.InGameweek(gw) {
			continue
		}
		sum[f.TeamH] += float64(f.TeamHDifficulty)
		count[f.TeamH]++
		sum[f.TeamA] += float64(f.TeamADifficulty)
		count[f.TeamA]++
	}
	out := make(map[int]float64, len(sum))
	for team, s := range sum {
		out[team] = s / float64(count[team])
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
