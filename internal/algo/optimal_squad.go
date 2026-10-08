package algo

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

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

// fplQuota is FPL's squad shape: 2 GKP, 5 DEF, 5 MID, 3 FWD.
var fplQuota = [5]int{0, 2, 5, 5, 3}

// benchWeight is what a bench player's projected points count for in the
// squad objective (#31). A bench player scores only when a starter misses
// out, so counting the bench at full value made the optimiser spend on
// players who rarely score. 0.1 still breaks ties toward a playable bench.
const benchWeight = 0.1

// fplLineup is the objective both optimisers use: the best starting XI in a
// valid FPL formation (1 GKP, 3-5 DEF, 2-5 MID, 1-3 FWD) at full value, plus
// the four bench players at benchWeight.
var fplLineup = LineupRules{
	Size:        11,
	Min:         [5]int{0, 1, minDef, minMid, minFwd},
	Max:         [5]int{0, 1, maxDef, maxMid, maxFwd},
	BenchWeight: benchWeight,
}

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
// The squad is chosen to maximize ProjectedPoints: over the projection
// window, the best starting XI in a valid formation at full value plus the
// four bench players at benchWeight (see fplLineup). Alongside the squad it
// suggests who to start in the target gameweek, in what formation, the bench
// order, and a captain and vice-captain. That lineup uses the target
// gameweek's projections alone, so a player who blanks that week sits on the
// bench, and it can differ from the XI behind ProjectedPoints. The captain
// uses captain_pick's scoring for the target gameweek, and only among
// starters who have a fixture in it.
type OptimalSquadResult struct {
	Gameweek        int     `json:"gameweek"`
	GameweeksAhead  int     `json:"gameweeks_ahead"`
	BudgetM         float64 `json:"budget_m"`
	TotalCostM      float64 `json:"total_cost_m"`
	ProjectedPoints float64 `json:"projected_points"`
	// ProjectedPointsBasis says in words what ProjectedPoints sums.
	ProjectedPointsBasis string `json:"projected_points_basis"`
	// StartingXIGameweekPoints is the suggested XI's projected points in the
	// target gameweek alone.
	StartingXIGameweekPoints float64            `json:"starting_xi_gameweek_points"`
	Optimal                  bool               `json:"optimal"`
	PoolNote                 string             `json:"pool_note"`
	Squad                    []OptimalSquadSlot `json:"squad"`

	// StartingXI and BenchOrder hold player ids from Squad, picked on the
	// target gameweek's projections. StartingXI lists the goalkeeper first;
	// BenchOrder opens with the spare goalkeeper and then runs by descending
	// projected points for that gameweek.
	StartingXI []int  `json:"starting_xi"`
	Formation  string `json:"formation"`
	BenchOrder []int  `json:"bench_order"`
	// The captain and vice-captain come from the starting XI and are nil when
	// none of them has a fixture in the target gameweek.
	RecommendedCaptain     *LineupCaptain `json:"recommended_captain"`
	RecommendedViceCaptain *LineupCaptain `json:"recommended_vice_captain"`

	// Include is set only when include_player_ids was given.
	Include *IncludeCost `json:"include,omitempty"`
}

// LineupCaptain is a captaincy recommendation for a suggested lineup.
type LineupCaptain struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Team  string  `json:"team"`
	Score float64 `json:"score"`
	// Reason is captain_pick's own reasoning for the player.
	Reason string `json:"reason"`
}

// OptimalSquadSlot is one selected player.
type OptimalSquadSlot struct {
	ID              int     `json:"id"`
	Name            string  `json:"name"`
	Team            string  `json:"team"`
	Position        string  `json:"position"`
	CostM           float64 `json:"cost_m"` // market price
	ProjectedPoints float64 `json:"projected_points"`
	// SellingPriceM is set only on optimal_transfers' transfers_out legs,
	// when the manager's transfer history could be fetched: what FPL pays
	// for the player, as opposed to CostM, the market price.
	SellingPriceM float64 `json:"selling_price_m,omitempty"`
	// PriceRisk is set only on optimal_transfers legs, when price_predictions
	// flags the player. It is informational and never affects selection.
	PriceRisk string `json:"price_risk,omitempty"`
}

// SquadRequestError is a caller mistake in an optimal_squad request, such
// as a forced player set no squad can hold. Its message is written for the
// caller and safe to show as is.
type SquadRequestError struct{ Msg string }

func (e *SquadRequestError) Error() string { return e.Msg }

func squadRequestErrorf(format string, args ...any) error {
	return &SquadRequestError{Msg: fmt.Sprintf(format, args...)}
}

// OptimalSquad builds the projected-points-maximizing 15-man squad under
// budgetTenths (tenths of a million), optionally starting the 5-gameweek
// projection window from gameweek (defaults to the next gameweek),
// excluding excludeIDs from consideration entirely, and always including
// includeIDs.
//
// With includeIDs, it also solves without them, in parallel, so the result
// can show what forcing them in costs. A forced set that no squad can hold
// returns a *SquadRequestError.
func (e *Engine) OptimalSquad(ctx context.Context, budgetTenths int, gameweek *int, excludeIDs, includeIDs []int) (*OptimalSquadResult, error) {
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

	byID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		byID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)

	included, err := checkIncluded(includeIDs, excluded, byID, teams, budgetTenths)
	if err != nil {
		return nil, err
	}

	// Included players always reach Solve, even outside the pool cap.
	candidates := buildCandidates(bootstrap.Elements, window, excluded, toSet(included))
	constraints := SquadConstraints{
		BudgetTenths:  budgetTenths,
		PositionQuota: fplQuota,
		MaxPerClub:    3,
		MaxChanges:    -1,
		TimeLimit:     optimalSquadTimeLimit,
		Lineup:        &fplLineup,
	}

	// The two searches run in parallel so the forced comparison costs no
	// extra wall time.
	var result, unforced Result
	var g errgroup.Group
	g.Go(func() error {
		c := constraints
		c.Forced = included
		r, err := Solve(candidates, c)
		if err != nil && len(included) > 0 {
			cost := 0
			for _, id := range included {
				cost += byID[id].NowCost
			}
			return squadRequestErrorf(
				"No valid squad includes all of include_player_ids: they cost £%.1fm of the £%.1fm budget, which leaves too little to fill the other %d places.",
				float64(cost)/10, float64(budgetTenths)/10, 15-len(included))
		}
		result = r
		return err
	})
	if len(included) > 0 {
		g.Go(func() error {
			// Built without the forced players' pool exemption, so the
			// comparison is with the squad optimal_squad picks unprompted.
			r, err := Solve(buildCandidates(bootstrap.Elements, window, excluded, nil), constraints)
			unforced = r
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	// result.Squad is already sorted by position then ID (bnb.go's
	// deterministic tie-break), matching the desired GKP/DEF/MID/FWD display
	// order exactly, so no re-sort is needed here.
	squad := make([]OptimalSquadSlot, 0, len(result.Squad))
	totalCost := 0
	for _, c := range result.Squad {
		squad = append(squad, slotOf(byID[c.ID], teams, c.PriceTenths, c.Value))
		totalCost += c.PriceTenths
	}

	lineup, xiPoints := chooseGameweekLineup(squad, byID, fixtures, gw)
	captain, vice := e.pickLineupCaptains(lineup.XI, byID, teams, buildFixtureMap(fixtures, gw, teams), gw)

	return &OptimalSquadResult{
		StartingXI: lineup.XI, Formation: lineup.Formation, BenchOrder: lineup.Bench,
		RecommendedCaptain: captain, RecommendedViceCaptain: vice,
		Gameweek:                 gw,
		GameweeksAhead:           xpHorizonGWs,
		BudgetM:                  float64(budgetTenths) / 10,
		TotalCostM:               float64(totalCost) / 10,
		ProjectedPoints:          Round(result.Value, 2),
		ProjectedPointsBasis:     projectedPointsBasis(gw),
		StartingXIGameweekPoints: xiPoints,
		Optimal:                  result.Optimal,
		PoolNote: fmt.Sprintf(
			"Considered the top %d/%d/%d/%d GKP/DEF/MID/FWD candidates by projected points — a near-optimal, not certified-optimal-over-every-player, approximation needed to keep the search tractable.",
			candidatePoolCap[1], candidatePoolCap[2], candidatePoolCap[3], candidatePoolCap[4]),
		Squad:   squad,
		Include: includeCost(included, result, unforced),
	}, nil
}

// IncludeCost reports what forcing include_player_ids into the squad costs.
type IncludeCost struct {
	PlayerIDs []int `json:"player_ids"`
	// ProjectedPointsWithout is the best squad's projected_points without
	// the forced players, on the same basis as projected_points.
	ProjectedPointsWithout float64 `json:"projected_points_without"`
	// PointsCost is ProjectedPointsWithout minus projected_points: what the
	// constraint gives up. Zero when the best squad already has them.
	PointsCost float64 `json:"points_cost"`
	// Optimal is false when either search hit its time limit, so the cost is
	// an estimate.
	Optimal bool `json:"optimal"`
}

// includeCost compares the forced and unforced searches. The forced squad
// is also a valid unforced squad, so a time-limited unforced search that
// scored lower is replaced by the forced value: the cost is never negative.
func includeCost(included []int, forced, unforced Result) *IncludeCost {
	if len(included) == 0 {
		return nil
	}
	without := max(unforced.Value, forced.Value)
	return &IncludeCost{
		PlayerIDs:              included,
		ProjectedPointsWithout: Round(without, 2),
		PointsCost:             Round(without-forced.Value, 2),
		Optimal:                forced.Optimal && unforced.Optimal,
	}
}

// checkIncluded validates include_player_ids before any search, so an
// impossible request gets an error naming the problem. It returns the ids
// deduplicated, in request order.
func checkIncluded(includeIDs []int, excluded map[int]bool, byID map[int]*fpl.Player, teams map[int]*fpl.Team, budgetTenths int) ([]int, error) {
	var out []int
	seen := map[int]bool{}
	byPos := map[int][]string{}
	byClub := map[int][]string{}
	cost := 0
	for _, id := range includeIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		p := byID[id]
		if p == nil {
			return nil, squadRequestErrorf("include_player_ids: no player has id %d.", id)
		}
		if excluded[id] {
			return nil, squadRequestErrorf("Player %d (%s) is in both include_player_ids and exclude_player_ids.", id, p.WebName)
		}
		out = append(out, id)
		byPos[p.ElementType] = append(byPos[p.ElementType], p.WebName)
		byClub[p.Team] = append(byClub[p.Team], p.WebName)
		cost += p.NowCost
	}
	for pos := 1; pos <= numPositions; pos++ {
		if n := len(byPos[pos]); n > fplQuota[pos] {
			return nil, squadRequestErrorf("include_player_ids has %d %s players (%s); a squad holds %d.",
				n, Position(pos), strings.Join(byPos[pos], ", "), fplQuota[pos])
		}
	}
	for club, names := range byClub {
		if len(names) > 3 {
			return nil, squadRequestErrorf("include_player_ids has %d %s players (%s); a squad holds at most 3 from one club.",
				len(names), shortName(teams[club]), strings.Join(names, ", "))
		}
	}
	if cost > budgetTenths {
		return nil, squadRequestErrorf("include_player_ids cost £%.1fm together, over the £%.1fm budget.", float64(cost)/10, float64(budgetTenths)/10)
	}
	return out, nil
}

// projectedPointsBasis describes what projected_points sums, for the
// window starting at gw.
func projectedPointsBasis(gw int) string {
	return fmt.Sprintf(
		"Projected points over gameweeks %d-%d for the best starting XI in a valid formation, plus %g x the four bench players' projected points. "+
			"Bench players count at a discount because they only score when a starter misses out.",
		gw, gw+xpHorizonGWs-1, benchWeight)
}

// chooseGameweekLineup picks the starting XI for gw alone: each player is
// re-projected over that one gameweek, so a player who blanks sits on the
// bench. It returns the lineup and the XI's projected points for gw.
func chooseGameweekLineup(squad []OptimalSquadSlot, byID map[int]*fpl.Player, fixtures []fpl.Fixture, gw int) (lineup, float64) {
	window := buildProjectionWindow(fixtures, gw, 1)
	gwSlots := make([]OptimalSquadSlot, len(squad))
	points := make(map[int]float64, len(squad))
	for i, slot := range squad {
		p := byID[slot.ID]
		slot.ProjectedPoints = projectExpectedPoints(p, window[p.Team])
		points[slot.ID] = slot.ProjectedPoints
		gwSlots[i] = slot
	}
	l := chooseLineup(gwSlots)
	total := 0.0
	for _, id := range l.XI {
		total += points[id]
	}
	return l, Round(total, 2)
}

// pickLineupCaptains scores every starter with captain_pick's own scorePlayer
// and returns the top two as captain and vice-captain. Starters with no
// fixture that gameweek are skipped: they cannot score.
func (e *Engine) pickLineupCaptains(xi []int, byID map[int]*fpl.Player, teams map[int]*fpl.Team, fixtureMap map[int][]TeamFixture, gw int) (captain, vice *LineupCaptain) {
	var scored []scoredPlayer
	for _, id := range xi {
		p := byID[id]
		pf := fixtureMap[p.Team]
		if len(pf) == 0 {
			continue
		}
		scored = append(scored, scoredPlayer{e.scorePlayer(p, pf), p, pf})
	}
	// Stable, so equal scores keep starting-XI order.
	slices.SortStableFunc(scored, func(a, b scoredPlayer) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		default:
			return 0
		}
	})

	toCaptain := func(s scoredPlayer, rank int) *LineupCaptain {
		pick := e.buildPick(s, gw, teams, rank)
		return &LineupCaptain{ID: s.player.ID, Name: s.player.WebName, Team: shortName(teams[s.player.Team]), Score: pick.Score, Reason: pick.Reasoning}
	}
	if len(scored) > 0 {
		captain = toCaptain(scored[0], 1)
	}
	if len(scored) > 1 {
		vice = toCaptain(scored[1], 2)
	}
	return captain, vice
}

// PositionOrder fixes GKP/DEF/MID/FWD as squad display order.
var PositionOrder = map[string]int{"GKP": 1, "DEF": 2, "MID": 3, "FWD": 4}
