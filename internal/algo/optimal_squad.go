package algo

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
)

// optimal_squad builds a 15-man squad from scratch: translate the bootstrap
// into bnb.Candidate values (see buildCandidates), then hand them to Solve.
//
// Candidate.Value is projectExpectedPoints summed over the next 5
// gameweeks (hit_analyzer.go) — already a portable, additive, per-player
// points projection built for is_hit_worth_it, so squad selection reuses it
// rather than introducing a second, differently-calibrated formula.

// candidatePoolCap bounds how many candidates per position reach Solve.
// Real bootstraps carry roughly 460-510 status=="a" players — 2.5-3x the
// ~180-candidate scale bnb_test.go's realisticFPLCandidates proves tractable
// within a few seconds — so a per-position top-K prefilter by Value is
// required for tractability, not optional. The split below is the exact one
// realisticFPLCandidates uses, generous relative to the 2/5/5/3 quota (12x
// headroom in every position), so the true optimum is very unlikely to be
// excluded — but this is a documented approximation, not a certified bound
// the way Solve's own pruning is: Result.Optimal only promises optimality
// within whatever pool reached it.
var candidatePoolCap = [5]int{0, 24, 60, 60, 36}

// optimalSquadTimeLimit matches the exact value bnb_test.go's
// TestSolveAtRealisticFPLScale already proves safe at this candidate count.
const optimalSquadTimeLimit = 8 * time.Second

// buildCandidates translates bootstrap players into Candidate values.
//
// Filtering, in priority order:
//   - excludeIDs always wins, even over alwaysInclude — an explicit
//     caller-specified exclusion is a stronger signal than "currently owned".
//   - Status=="u" (left the league) is hard-excluded unless alwaysInclude.
//   - Everything else stays a candidate: projectExpectedPoints already
//     drives an unavailable player's Value toward zero, so a second
//     hard-coded availability rule would be redundant and would wrongly
//     exclude a marginal recovery case (e.g. 75% chance of playing).
//   - Per position, only the top candidatePoolCap[pos] candidates by Value
//     survive, except anything in alwaysInclude, which always survives
//     regardless of rank — optimal_transfers uses this to guarantee every
//     one of the manager's current 15 players is representable, even an
//     injured one or one that would otherwise fall outside the pool cap.
func buildCandidates(elements []fpl.Player, window map[int][]projectionFixture, excludeIDs, alwaysInclude map[int]bool) []Candidate {
	byPosition := make([][]Candidate, numPositions+1)

	for i := range elements {
		p := &elements[i]
		if excludeIDs[p.ID] {
			continue
		}
		if p.Status == "u" && !alwaysInclude[p.ID] {
			continue
		}
		if p.ElementType < 1 || p.ElementType > numPositions {
			continue
		}

		byPosition[p.ElementType] = append(byPosition[p.ElementType], Candidate{
			ID:          p.ID,
			Position:    p.ElementType,
			PriceTenths: p.NowCost,
			Club:        p.Team,
			Value:       projectExpectedPoints(p, window[p.Team]),
		})
	}

	out := make([]Candidate, 0, len(elements))
	for pos := 1; pos <= numPositions; pos++ {
		cands := byPosition[pos]
		slices.SortStableFunc(cands, func(a, b Candidate) int {
			switch {
			case a.Value > b.Value:
				return -1
			case a.Value < b.Value:
				return 1
			default:
				return 0
			}
		})

		poolCap := candidatePoolCap[pos]
		for i, c := range cands {
			if i < poolCap || alwaysInclude[c.ID] {
				out = append(out, c)
			}
		}
	}
	return out
}

// OptimalSquadResult is optimal_squad's response shape.
//
// This selects the 15-man squad only — not a starting XI or captain, which
// captain_pick and the existing chip logic already cover.
type OptimalSquadResult struct {
	Gameweek        int                `json:"gameweek"`
	GameweeksAhead  int                `json:"gameweeks_ahead"`
	BudgetM         float64            `json:"budget_m"`
	TotalCostM      float64            `json:"total_cost_m"`
	ProjectedPoints float64            `json:"projected_points"`
	Optimal         bool               `json:"optimal"`
	PoolNote        string             `json:"pool_note"`
	Squad           []OptimalSquadSlot `json:"squad"`
}

// OptimalSquadSlot is one selected player.
type OptimalSquadSlot struct {
	ID              int     `json:"id"`
	Name            string  `json:"name"`
	Team            string  `json:"team"`
	Position        string  `json:"position"`
	CostM           float64 `json:"cost_m"`
	ProjectedPoints float64 `json:"projected_points"`
}

// OptimalSquad builds the projected-points-maximizing 15-man squad under
// budgetTenths (tenths of a million), optionally starting the 5-gameweek
// projection window from gameweek (defaults to the next gameweek) and
// excluding excludeIDs from consideration entirely.
func (e *Engine) OptimalSquad(ctx context.Context, budgetTenths int, gameweek *int, excludeIDs []int) (*OptimalSquadResult, error) {
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	fixtures, err := e.client.Fixtures(ctx)
	if err != nil {
		return nil, err
	}

	gw := bootstrap.NextGameweek()
	if gameweek != nil {
		gw = *gameweek
	}
	window := buildProjectionWindow(fixtures, gw, xpHorizonGWs)

	excluded := make(map[int]bool, len(excludeIDs))
	for _, id := range excludeIDs {
		excluded[id] = true
	}

	candidates := buildCandidates(bootstrap.Elements, window, excluded, nil)

	result, err := Solve(candidates, SquadConstraints{
		BudgetTenths:  budgetTenths,
		PositionQuota: [5]int{0, 2, 5, 5, 3},
		MaxPerClub:    3,
		MaxChanges:    -1,
		TimeLimit:     optimalSquadTimeLimit,
	})
	if err != nil {
		return nil, err
	}

	byID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		byID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)

	// result.Squad is already sorted by position then ID (bnb.go's
	// deterministic tie-break), matching the desired GKP/DEF/MID/FWD display
	// order exactly, so no re-sort is needed here.
	squad := make([]OptimalSquadSlot, 0, len(result.Squad))
	totalCost := 0
	for _, c := range result.Squad {
		squad = append(squad, slotOf(byID[c.ID], teams, c.PriceTenths, c.Value))
		totalCost += c.PriceTenths
	}

	return &OptimalSquadResult{
		Gameweek:        gw,
		GameweeksAhead:  xpHorizonGWs,
		BudgetM:         float64(budgetTenths) / 10,
		TotalCostM:      float64(totalCost) / 10,
		ProjectedPoints: Round(result.Value, 2),
		Optimal:         result.Optimal,
		PoolNote: fmt.Sprintf(
			"Considered the top %d/%d/%d/%d GKP/DEF/MID/FWD candidates by projected points — a near-optimal, not certified-optimal-over-every-player, approximation needed to keep the search tractable.",
			candidatePoolCap[1], candidatePoolCap[2], candidatePoolCap[3], candidatePoolCap[4]),
		Squad: squad,
	}, nil
}

// PositionOrder fixes GKP/DEF/MID/FWD as squad display order.
var PositionOrder = map[string]int{"GKP": 1, "DEF": 2, "MID": 3, "FWD": 4}
