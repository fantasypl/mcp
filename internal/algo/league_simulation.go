package algo

import (
	"math"
	"math/rand/v2"

	"github.com/fantasypl/mcp/internal/fpl"
)

// League win probability by Monte Carlo (issue #47).
//
// The old league_analyzer blended five heuristic scores and normalised them
// across the top 10 only. Its output could not be read as a real chance: in
// the recorded league (testdata/league_1092839) a leader 89 points clear with
// 28 gameweeks to play got 14.4%, and ranks 11 to 16 got nothing.
//
// The model now simulates the rest of the season:
//
//	final total = current total + sum over remaining gameweeks of a weekly score + chip allowance
//
// For each manager, the weekly score is Normal(mean, sd) from their own net
// gameweek scores this season (points minus hits, leaving out Bench Boost,
// Triple Captain and Free Hit weeks; see scoringWeeks), shrunk toward the
// league's pooled mean and variance by leagueShrinkGameweeks of "prior"
// gameweeks. Ten gameweeks is little evidence: without shrinkage a manager
// who started hot would be projected to stay hot for 28 more weeks. The sum
// of the remaining weeks is drawn in one step, Normal(R x mean, sqrt(R) x sd),
// which is exact for independent normal weeks. Each manager is drawn
// independently; the winner of each simulation is the highest final total,
// with ties shared.
//
// Unused chips from the current half of the season add a fixed allowance
// (leagueChipAllowance). Chips from the second half are ignored before
// GW20, because every manager gets the full set then, so they shift every
// total equally and cannot change who wins.
//
// The draw is seeded from the league ID and gameweek, so repeated calls in
// one gameweek return the same figures.
//
// What it leaves out: squad quality, fixtures, injuries and team value. Those
// stay in the per-manager detail rows as information. They would need a
// points model per squad to enter a simulation honestly.

const (
	// leagueSimulations is enough for the standard error of a 10%
	// probability to be about 0.2 points.
	leagueSimulations = 20_000

	// leagueShrinkGameweeks is how many gameweeks of league-average scoring
	// each manager's own record is blended with. A judgement call, not a
	// fitted value: after 10 gameweeks a manager's own mean gets half the
	// weight.
	leagueShrinkGameweeks = 10

	// maxModelledManagers is one page of FPL's standings. Every manager on
	// it is modelled; a larger league is reported as truncated.
	maxModelledManagers = 50
)

// leagueChipAllowance is the points an unused chip is assumed to add over
// the rest of the season. Judgement calls, not fitted values, chosen to be
// modest: a Bench Boost scores the bench once (FPL benches typically score
// 5 to 12), a Triple Captain adds one extra captain score, a Free Hit
// rescues one blank or double gameweek, and a Wildcard's gain is spread
// thinly over several weeks.
var leagueChipAllowance = map[string]float64{
	"bboost":   8,
	"3xc":      7,
	"freehit":  6,
	"wildcard": 4,
}

// leagueWinProbabilityMethod is the method statement returned with every
// league_analyzer result.
const leagueWinProbabilityMethod = "Monte Carlo, 20,000 simulated seasons. Each manager's remaining gameweeks are drawn from a normal distribution with the mean and spread of their own net gameweek scores this season (points minus hits, leaving out Bench Boost, Triple Captain and Free Hit weeks), blended with the league average as if each manager had 10 extra average gameweeks. Each unused chip from the current half of the season adds a fixed allowance (Bench Boost 8, Triple Captain 7, Free Hit 6, Wildcard 4). The win probability is the share of simulations a manager finishes top, with ties shared. Squad quality, fixtures and injuries are not in the model. A manager with no history is drawn at the league average."

// leagueSimEntry is one manager's input to the simulation.
type leagueSimEntry struct {
	TotalPoints int
	// NetScores are this season's gameweek scores after hits.
	NetScores []int
	// Chips are the unused chips of the current half of the season.
	Chips []string
	// HasHistory is false when the manager's history could not be
	// fetched; they are then drawn at the league average.
	HasHistory bool
}

// netGameweekScores returns each gameweek's points after hits.
// oneOffChips boost a single gameweek in a way that cannot recur from the
// same chip. A Wildcard changes the squad but scores like any other week.
var oneOffChips = map[string]bool{"bboost": true, "3xc": true, "freehit": true}

// scoringWeeks are the net gameweek scores the simulation learns a manager's
// weekly rate from. Bench Boost, Triple Captain and Free Hit weeks are left
// out: a manager who has already played them would otherwise carry their
// boost into every remaining week, while the chip allowance separately
// credits managers who still hold them.
func scoringWeeks(h *fpl.TeamHistory) []int {
	chipWeek := make(map[int]bool, len(h.Chips))
	for _, c := range h.Chips {
		if oneOffChips[c.Name] {
			chipWeek[c.Event] = true
		}
	}
	var kept []fpl.HistoryGameweek
	for _, g := range h.Current {
		if !chipWeek[g.Event] {
			kept = append(kept, g)
		}
	}
	return netGameweekScores(kept)
}

func netGameweekScores(history []fpl.HistoryGameweek) []int {
	out := make([]int, 0, len(history))
	for _, g := range history {
		out = append(out, g.Points-g.EventTransfersCost)
	}
	return out
}

// simulateLeagueWinners returns each manager's win probability in percent,
// in the order given, rounded to one decimal.
func simulateLeagueWinners(entries []leagueSimEntry, gwsRemaining int, seed uint64) []float64 {
	probs := make([]float64, len(entries))
	if len(entries) == 0 {
		return probs
	}

	// With nothing left to play, the current leaders have won.
	if gwsRemaining <= 0 {
		best := entries[0].TotalPoints
		for _, e := range entries {
			best = max(best, e.TotalPoints)
		}
		leaders := 0
		for _, e := range entries {
			if e.TotalPoints == best {
				leaders++
			}
		}
		for i, e := range entries {
			if e.TotalPoints == best {
				probs[i] = Round(100/float64(leaders), 1)
			}
		}
		return probs
	}

	leagueMean, leagueVar := pooledScoreStats(entries)
	remaining := float64(gwsRemaining)

	// Each manager's distribution for the whole remaining season.
	centre := make([]float64, len(entries))
	spread := make([]float64, len(entries))
	for i, e := range entries {
		mean, variance := leagueMean, leagueVar
		if e.HasHistory && len(e.NetScores) > 0 {
			mean, variance = shrunkScoreStats(e.NetScores, leagueMean, leagueVar)
		}
		chips := 0.0
		for _, c := range e.Chips {
			chips += leagueChipAllowance[c]
		}
		centre[i] = float64(e.TotalPoints) + remaining*mean + chips
		spread[i] = math.Sqrt(remaining * variance)
	}

	rng := rand.New(rand.NewPCG(seed, uint64(gwsRemaining)))
	wins := make([]float64, len(entries))
	finals := make([]float64, len(entries))
	for s := 0; s < leagueSimulations; s++ {
		best := math.Inf(-1)
		for i := range entries {
			finals[i] = centre[i] + spread[i]*rng.NormFloat64()
			best = math.Max(best, finals[i])
		}
		// Draws are continuous, so exact ties are rare, but share them.
		leaders := 0
		for i := range finals {
			if finals[i] == best {
				leaders++
			}
		}
		for i := range finals {
			if finals[i] == best {
				wins[i] += 1 / float64(leaders)
			}
		}
	}
	for i := range wins {
		probs[i] = Round(wins[i]/leagueSimulations*100, 1)
	}
	return probs
}

// pooledScoreStats is the mean and variance of every manager's gameweek
// scores pooled together. With no history at all it falls back to a typical
// FPL gameweek (mean 50, sd 15), so the simulation still runs.
func pooledScoreStats(entries []leagueSimEntry) (mean, variance float64) {
	var all []int
	for _, e := range entries {
		if e.HasHistory {
			all = append(all, e.NetScores...)
		}
	}
	if len(all) < 2 {
		return 50, 15 * 15
	}
	return meanVariance(all)
}

// shrunkScoreStats blends a manager's own mean and variance with the
// league's, weighting the league as leagueShrinkGameweeks gameweeks.
func shrunkScoreStats(scores []int, leagueMean, leagueVar float64) (mean, variance float64) {
	n := float64(len(scores))
	k := float64(leagueShrinkGameweeks)
	// One gameweek gives a mean but no spread, so its variance is the
	// league's.
	var ownMean, ownVar float64
	if len(scores) >= 2 {
		ownMean, ownVar = meanVariance(scores)
	} else {
		ownMean, ownVar = float64(scores[0]), leagueVar
	}
	mean = (n*ownMean + k*leagueMean) / (n + k)
	variance = (n*ownVar + k*leagueVar) / (n + k)
	return mean, variance
}

// meanVariance is the sample mean and (n-1) variance.
func meanVariance(xs []int) (mean, variance float64) {
	sum := 0.0
	for _, x := range xs {
		sum += float64(x)
	}
	mean = sum / float64(len(xs))
	ss := 0.0
	for _, x := range xs {
		d := float64(x) - mean
		ss += d * d
	}
	return mean, ss / float64(len(xs)-1)
}

// LeagueWinChance is one manager's row in the league-wide probability table.
type LeagueWinChance struct {
	Rank             int     `json:"rank"`
	ManagerName      string  `json:"manager_name"`
	TeamName         string  `json:"team_name"`
	TeamID           int     `json:"team_id"`
	TotalPoints      int     `json:"total_points"`
	PointsFromLeader int     `json:"points_from_leader"`
	WinProbability   float64 `json:"win_probability"`
	// Basis is "own history (N gameweeks)", or "league average" when the
	// history could not be fetched.
	Basis string `json:"basis"`
}

// LeagueCallerRow is the caller's position when league_analyzer is given a
// team_id.
type LeagueCallerRow struct {
	TeamID            int `json:"team_id"`
	Rank              int `json:"rank,omitempty"`
	TotalPoints       int `json:"total_points,omitempty"`
	PointsGapToLeader int `json:"points_gap_to_leader,omitempty"`
	// RequiredPPGOverLeader is the gap divided by the gameweeks left: how
	// many more points than the leader the caller must score each week to
	// draw level by GW38.
	RequiredPPGOverLeader float64 `json:"required_ppg_over_leader,omitempty"`
	WinProbability        float64 `json:"win_probability"`
	Note                  string  `json:"note,omitempty"`
	// Gaps is the caller's gap to every other modelled manager (#53).
	Gaps []LeagueGap `json:"gaps,omitempty"`
}
