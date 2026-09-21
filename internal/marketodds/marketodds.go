// Package marketodds turns bookmaker match odds into the probabilities FPL
// decisions actually need: each team's expected goals and its chance of a
// clean sheet.
//
// The market prices in team news, form and rest that FPL's static fixture
// difficulty ignores, so an odds-implied clean-sheet probability is a
// candidate replacement for the FDR heuristic. Whether it actually beats
// FDR is measured by `fplctl odds-backtest`, not assumed.
//
// The model is deliberately simple: strip the bookmaker margin, then fit two
// independent Poisson goal rates to the three prices most bookmakers quote
// (home win, away win, over 2.5 goals). Independence understates the
// draw-heavy 0-0/1-1 cells slightly; the backtest reports calibration so any
// resulting bias is visible.
package marketodds

import (
	"errors"
	"math"
)

// Prices are decimal odds for one match. Zero means "not quoted".
type Prices struct {
	Home, Draw, Away float64
	Over25, Under25  float64
}

// Probs are margin-free probabilities.
type Probs struct {
	Home, Draw, Away float64
	Over25           float64 // zero when no totals market was quoted
	HasTotals        bool
}

// Key identifies a match by FPL team names (Home v Away).
type Key struct{ Home, Away string }

// MatchModel is the fitted result for one match.
type MatchModel struct {
	HomeXG, AwayXG float64 // expected goals scored by each side
	HomeCS, AwayCS float64 // P(home keeps a clean sheet), P(away ...)
	Probs          Probs
}

// ErrNoPrices means the 1X2 market was missing or invalid.
var ErrNoPrices = errors.New("marketodds: missing or invalid 1X2 prices")

// defaultTotalGoals is the fallback league-average goals per match, used to
// pin the total when no over/under market was quoted. EPL averages sit near
// 2.75 goals a match across recent seasons.
const defaultTotalGoals = 2.75

// Devig removes the bookmaker margin by proportional scaling — the standard
// simple approach. It errors if the 1X2 prices are unusable.
func Devig(p Prices) (Probs, error) {
	if p.Home <= 1 || p.Draw <= 1 || p.Away <= 1 {
		return Probs{}, ErrNoPrices
	}
	ih, id, ia := 1/p.Home, 1/p.Draw, 1/p.Away
	sum := ih + id + ia
	out := Probs{Home: ih / sum, Draw: id / sum, Away: ia / sum}
	if p.Over25 > 1 && p.Under25 > 1 {
		io, iu := 1/p.Over25, 1/p.Under25
		out.Over25 = io / (io + iu)
		out.HasTotals = true
	}
	return out, nil
}

// poissonPMF returns P(X=k) for a Poisson variable with mean lambda.
func poissonPMF(lambda float64, k int) float64 {
	p := math.Exp(-lambda)
	for i := 1; i <= k; i++ {
		p *= lambda / float64(i)
	}
	return p
}

const maxGoals = 12

// outcome returns P(home win), P(draw), P(away win) and P(total goals >= 3)
// under independent Poisson goals.
func outcome(lh, la float64) (h, d, a, over float64) {
	var ph, pa [maxGoals + 1]float64
	for k := 0; k <= maxGoals; k++ {
		ph[k], pa[k] = poissonPMF(lh, k), poissonPMF(la, k)
	}
	for i := 0; i <= maxGoals; i++ {
		for j := 0; j <= maxGoals; j++ {
			p := ph[i] * pa[j]
			switch {
			case i > j:
				h += p
			case i == j:
				d += p
			default:
				a += p
			}
			if i+j >= 3 {
				over += p
			}
		}
	}
	return h, d, a, over
}

// Fit finds the goal rates whose Poisson outcome probabilities best match
// pr, by coarse-to-fine grid search on squared error. A grid search is used
// over a solver because the objective is cheap, two-dimensional, and this
// runs on ~10 matches a gameweek — robustness beats speed.
func Fit(pr Probs) MatchModel {
	err := func(lh, la float64) float64 {
		h, _, a, over := outcome(lh, la)
		e := (h-pr.Home)*(h-pr.Home) + (a-pr.Away)*(a-pr.Away)
		if pr.HasTotals {
			e += (over - pr.Over25) * (over - pr.Over25)
		} else {
			// Pin the total instead: penalise distance from league average.
			t := lh + la - defaultTotalGoals
			e += 0.01 * t * t
		}
		return e
	}

	bestH, bestA := 1.4, 1.2
	best := math.Inf(1)
	lo1, hi1, lo2, hi2 := 0.05, 5.0, 0.05, 5.0
	for _, steps := range []int{40, 20, 20, 20} {
		d1 := (hi1 - lo1) / float64(steps)
		d2 := (hi2 - lo2) / float64(steps)
		for i := 0; i <= steps; i++ {
			for j := 0; j <= steps; j++ {
				lh, la := lo1+float64(i)*d1, lo2+float64(j)*d2
				if e := err(lh, la); e < best {
					best, bestH, bestA = e, lh, la
				}
			}
		}
		lo1, hi1 = math.Max(0.02, bestH-2*d1), bestH+2*d1
		lo2, hi2 = math.Max(0.02, bestA-2*d2), bestA+2*d2
	}
	return MatchModel{
		HomeXG: bestH, AwayXG: bestA,
		HomeCS: math.Exp(-bestA), AwayCS: math.Exp(-bestH),
		Probs: pr,
	}
}

// Model devigs and fits in one step.
func Model(p Prices) (MatchModel, error) {
	pr, err := Devig(p)
	if err != nil {
		return MatchModel{}, err
	}
	return Fit(pr), nil
}
