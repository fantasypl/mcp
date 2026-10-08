package algo

import (
	"context"
	"fmt"
	"slices"

	"github.com/fantasypl/mcp/internal/fpl"
)

// optimal_transfers' Wildcard mode (#21) answers "what is the best squad I can
// reach from my current one?". It differs from optimal_squad in its budget:
// bank plus the selling prices of the current 15, which is what FPL offers on
// a Wildcard, with kept players priced at their selling price and incoming
// players at their market price. Every transfer is free, so it runs one
// search with unlimited changes and no hit cost.

// WildcardResult is optimal_transfers' response shape when wildcard is set.
type WildcardResult struct {
	TeamID         int  `json:"team_id"`
	Gameweek       int  `json:"gameweek"`
	GameweeksAhead int  `json:"gameweeks_ahead"`
	Wildcard       bool `json:"wildcard"`
	// WildcardAvailable is false when the manager has no Wildcard left to
	// play. The search still runs, and WildcardNote says so.
	WildcardAvailable bool   `json:"wildcard_available"`
	WildcardNote      string `json:"wildcard_note"`

	BudgetM     float64 `json:"budget_m"`
	BudgetUsedM float64 `json:"budget_used_m"`
	BudgetLeftM float64 `json:"budget_left_m"`
	BudgetNote  string  `json:"budget_note"`
	PoolNote    string  `json:"pool_note"`

	// ProjectedPoints and CurrentProjectedPoints are on the same basis
	// (ProjectedPointsBasis), so ProjectedGain is their difference.
	ProjectedPointsBasis   string  `json:"projected_points_basis"`
	ProjectedPoints        float64 `json:"projected_points"`
	CurrentProjectedPoints float64 `json:"current_projected_points"`
	ProjectedGain          float64 `json:"projected_gain"`

	NumTransfers    int                `json:"num_transfers"`
	TransfersOut    []OptimalSquadSlot `json:"transfers_out"`
	TransfersIn     []OptimalSquadSlot `json:"transfers_in"`
	Confidence      string             `json:"confidence"`
	ConfidenceNotes []string           `json:"confidence_notes"`
	PriceRiskNote   string             `json:"price_risk_note,omitempty"`

	// Squad is the full target squad. StartingXI, Formation, BenchOrder and
	// the captains are for the target gameweek alone, as in optimal_squad.
	Squad                    []OptimalSquadSlot `json:"squad"`
	StartingXI               []int              `json:"starting_xi"`
	Formation                string             `json:"formation"`
	BenchOrder               []int              `json:"bench_order"`
	StartingXIGameweekPoints float64            `json:"starting_xi_gameweek_points"`
	RecommendedCaptain       *LineupCaptain     `json:"recommended_captain"`
	RecommendedViceCaptain   *LineupCaptain     `json:"recommended_vice_captain"`

	Optimal     bool   `json:"optimal"`
	Partial     bool   `json:"partial"`
	PartialNote string `json:"partial_note,omitempty"`
}

const (
	wildcardNoteText = "Wildcard mode: every transfer is free, so there is no hit cost and allow_hits does not apply."
	// wildcardSellingNote is added to the budget note when selling prices
	// were available.
	wildcardSellingNote = " This matches the budget FPL offers on a Wildcard. Selling prices are computed from your public transfer history, not read from your FPL account, so check the total against the FPL app before you confirm."
	wildcardPartialNote = "The search reached its time limit before proving this is the best squad, so it is the best found in time and a better one may exist. It is still a valid squad within budget."
)

// OptimalWildcard finds the best squad a manager can reach with a Wildcard.
// Like OptimalTransfers, it returns a *TransferError when the team's picks
// can't be fetched.
func (e *Engine) OptimalWildcard(ctx context.Context, teamID int, gameweek *int) (any, error) {
	tc, terr, err := e.loadTransferContext(ctx, teamID, gameweek)
	if err != nil {
		return nil, err
	}
	if terr != nil {
		return terr, nil
	}

	candidates := buildCandidates(tc.bootstrap.Elements, tc.window, nil, tc.lockedSet)
	priceAtSelling(candidates, tc.selling)
	constraints := SquadConstraints{
		BudgetTenths:  tc.budgetTenths,
		PositionQuota: fplQuota,
		MaxPerClub:    3,
		MaxChanges:    -1,
		TimeLimit:     optimalSquadTimeLimit,
		Lineup:        &fplLineup,
	}
	result, err := Solve(candidates, constraints)
	if err != nil {
		return nil, err
	}

	current := currentSquadCandidates(candidates, tc.lockedIDs)
	currentValue := squadObjective(current, constraints)
	// The current squad is itself a valid Wildcard squad when it fits the
	// budget. A search that stopped at its time limit below it would
	// otherwise recommend a worse squad, so keep the current one instead.
	if len(current) == len(tc.lockedIDs) && squadCost(current) <= tc.budgetTenths && result.Value < currentValue {
		result = Result{Squad: current, Value: currentValue, Optimal: false}
	}

	return e.buildWildcardResult(ctx, tc, teamID, result, currentValue), nil
}

// currentSquadCandidates returns the candidates for the current squad, priced
// as the solver prices them.
func currentSquadCandidates(candidates []Candidate, lockedIDs []int) []Candidate {
	locked := toSet(lockedIDs)
	var out []Candidate
	for _, c := range candidates {
		if locked[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

// squadObjective scores a squad exactly as Solve does under c: the best XI
// plus the bench at c.Lineup.BenchWeight.
func squadObjective(squad []Candidate, c SquadConstraints) float64 {
	var byPos [5][]float64
	for _, cnd := range squad {
		byPos[cnd.Position] = append(byPos[cnd.Position], cnd.Value)
	}
	return lineupValue(byPos, c)
}

func squadCost(squad []Candidate) int {
	total := 0
	for _, c := range squad {
		total += c.PriceTenths
	}
	return total
}

func (e *Engine) buildWildcardResult(ctx context.Context, tc *transferContext, teamID int, result Result, currentValue float64) *WildcardResult {
	priceRisks := e.priceRiskByPlayer(ctx)
	resultSet := make(map[int]bool, len(result.Squad))
	for _, c := range result.Squad {
		resultSet[c.ID] = true
	}

	// result.Squad is sorted by position then ID, so the squad and
	// transfers_in come out in display order.
	squad := make([]OptimalSquadSlot, 0, len(result.Squad))
	var transfersIn []OptimalSquadSlot
	var incoming []*fpl.Player
	for _, c := range result.Squad {
		p := tc.byID[c.ID]
		slot := slotOf(p, tc.teams, p.NowCost, c.Value)
		if tc.lockedSet[c.ID] {
			if sp, ok := tc.selling[c.ID]; ok {
				slot.SellingPriceM = float64(sp.SellingTenths) / 10
			}
		} else {
			inSlot := slot
			inSlot.PriceRisk = priceRiskFor(p, priceRisks)
			transfersIn = append(transfersIn, inSlot)
			incoming = append(incoming, p)
		}
		squad = append(squad, slot)
	}

	var transfersOut []OptimalSquadSlot
	for _, id := range tc.lockedIDs {
		if resultSet[id] {
			continue
		}
		p := tc.byID[id]
		slot := slotOf(p, tc.teams, p.NowCost, projectExpectedPoints(p, tc.window[p.Team]))
		if sp, ok := tc.selling[id]; ok {
			slot.SellingPriceM = float64(sp.SellingTenths) / 10
		}
		slot.PriceRisk = priceRiskFor(p, priceRisks)
		transfersOut = append(transfersOut, slot)
	}
	slices.SortStableFunc(transfersOut, func(a, b OptimalSquadSlot) int {
		return PositionOrder[a.Position] - PositionOrder[b.Position]
	})

	lineup, xiPoints := chooseGameweekLineup(squad, tc.byID, tc.fixtures, tc.gw)
	captain, vice := e.pickLineupCaptains(lineup.XI, tc.byID, tc.teams, buildFixtureMap(tc.fixtures, tc.gw, tc.teams), tc.gw)
	confidence, confidenceNotes := summarizeOptionConfidence(incoming, gameweeksPlayed(tc.bootstrap))

	priceRiskNote := ""
	if anyPriceRisk(transfersOut) || anyPriceRisk(transfersIn) {
		priceRiskNote = priceRiskNoteText
	}

	budgetNote := tc.budgetNote
	if tc.selling != nil {
		budgetNote += wildcardSellingNote
	}
	available, wcNote := wildcardAvailability(tc.mgr)

	used := squadCost(result.Squad)
	partialNote := ""
	if !result.Optimal {
		partialNote = wildcardPartialNote
	}

	return &WildcardResult{
		TeamID:                   teamID,
		Gameweek:                 tc.gw,
		GameweeksAhead:           xpHorizonGWs,
		Wildcard:                 true,
		WildcardAvailable:        available,
		WildcardNote:             wcNote,
		BudgetM:                  float64(tc.budgetTenths) / 10,
		BudgetUsedM:              float64(used) / 10,
		BudgetLeftM:              float64(tc.budgetTenths-used) / 10,
		BudgetNote:               budgetNote,
		PoolNote:                 fmt.Sprintf("Considered the top %d/%d/%d/%d GKP/DEF/MID/FWD candidates by projected points, plus every player already in your squad. This is a near-optimal approximation, not certified optimal over every player, needed to keep the search tractable.", candidatePoolCap[1], candidatePoolCap[2], candidatePoolCap[3], candidatePoolCap[4]),
		ProjectedPointsBasis:     projectedPointsBasis(tc.gw),
		ProjectedPoints:          Round(result.Value, 2),
		CurrentProjectedPoints:   Round(currentValue, 2),
		ProjectedGain:            Round(result.Value-currentValue, 2),
		NumTransfers:             len(transfersIn),
		TransfersOut:             transfersOut,
		TransfersIn:              transfersIn,
		Confidence:               confidence,
		ConfidenceNotes:          confidenceNotes,
		PriceRiskNote:            priceRiskNote,
		Squad:                    squad,
		StartingXI:               lineup.XI,
		Formation:                lineup.Formation,
		BenchOrder:               lineup.Bench,
		StartingXIGameweekPoints: xiPoints,
		RecommendedCaptain:       captain,
		RecommendedViceCaptain:   vice,
		Optimal:                  result.Optimal,
		Partial:                  !result.Optimal,
		PartialNote:              partialNote,
	}
}

// wildcardAvailability reports whether the manager can still play a Wildcard,
// with the note to show either way.
func wildcardAvailability(m *fpl.ManagerStatus) (bool, string) {
	if m.ChipActiveThisGW != nil && *m.ChipActiveThisGW == "wildcard" {
		return true, wildcardNoteText + " Your Wildcard is already active this gameweek."
	}
	if slices.Contains(m.ChipsRemaining, "wildcard") {
		return true, wildcardNoteText
	}
	return false, wildcardNoteText + " You have no Wildcard left to play in this half of the season, so this squad is out of reach without one."
}
