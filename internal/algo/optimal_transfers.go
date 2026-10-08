package algo

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/fantasypl/mcp/internal/fpl"
)

// optimal_transfers re-solves Solve against a manager's current 15 players
// as Locked candidates, sweeping MaxChanges across a small range around
// their free-transfer count so the points-vs-hit-cost tradeoff is visible
// rather than hidden behind a single verdict — the same "show the
// tradeoff, don't hide it" style is_hit_worth_it already uses.
//
// Candidate.Value is a pure per-player projection with no notion of "whose
// transfer count this is" — that context lives entirely in
// SquadConstraints.MaxChanges, which the kernel already tracks. So the hit
// cost is applied post-hoc (netProjectedPoints = Value - 4*paid transfers),
// not folded into the objective: doing that would need the kernel itself to
// know which transfers are "free" versus "paid," which isn't a property of
// a completed squad.

// maxHitsConsidered bounds how many paid transfers beyond the free
// allowance the sweep considers when allowHits is set: a small, fixed
// number of extra Solve calls, each capped at optimalSquadTimeLimit and run
// in parallel (see solveTransferSweep).
const maxHitsConsidered = 3

// OptimalTransfersResult is optimal_transfers' response shape.
type OptimalTransfersResult struct {
	TeamID        int     `json:"team_id"`
	Gameweek      int     `json:"gameweek"`
	FreeTransfers int     `json:"free_transfers"`
	BudgetM       float64 `json:"budget_m"`
	BudgetNote    string  `json:"budget_note"`
	PoolNote      string  `json:"pool_note"`
	// ProjectedPointsBasis says in words what each option's projected_points
	// sums: the best XI plus a discounted bench, as optimal_squad uses.
	ProjectedPointsBasis string               `json:"projected_points_basis"`
	BestNote             string               `json:"best_note"` // what the best and safest flags on each option mean
	Options              []TransferPlanOption `json:"options"`
	PriceRiskNote        string               `json:"price_risk_note,omitempty"` // explains price_risk; set only when a leg carries one
	// Partial is true when any option's search hit its time limit before
	// proving optimality (that option has optimal: false). PartialNote then
	// says what that means for the caller.
	Partial     bool   `json:"partial"`
	PartialNote string `json:"partial_note,omitempty"`
}

// TransferPlanOption is one point on the transfers-vs-hit-cost sweep.
type TransferPlanOption struct {
	NumTransfers int `json:"num_transfers"`
	HitCost      int `json:"hit_cost"`
	// ProjectedPoints is the resulting squad's value: the best XI's
	// projected points plus the bench at benchWeight (see
	// OptimalTransfersResult.ProjectedPointsBasis), before any hit cost.
	ProjectedPoints    float64 `json:"projected_points"`
	NetProjectedPoints float64 `json:"net_projected_points"`
	Optimal            bool    `json:"optimal"`
	// Best marks the highest net projected points. That is expected value,
	// not reliability: see Confidence and Safest.
	Best bool `json:"best"`
	// Confidence is how far the projection can be trusted: high, medium or
	// low, set by the weakest incoming player. See assessIncomingPlayer.
	Confidence string `json:"confidence"`
	// ConfidenceNotes name each incoming player behind a below-high rating.
	ConfidenceNotes []string `json:"confidence_notes"`
	// Safest marks the most reliable option, with net projected points
	// breaking ties. It is often, but not always, the same as Best.
	Safest       bool               `json:"safest"`
	TotalCostM   float64            `json:"total_cost_m"`
	TransfersOut []OptimalSquadSlot `json:"transfers_out"`
	TransfersIn  []OptimalSquadSlot `json:"transfers_in"`
}

// Confidence levels for an incoming player or a whole option.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// Thresholds behind assessIncomingPlayer. Minutes are judged as a share of
// those available (90 per finished gameweek), not as an absolute total, so a
// regular starter isn't rated "low" simply because the season is young.
const (
	lowMinutesShare    = 0.3
	mediumMinutesShare = 0.6
	// Until this many gameweeks are finished, minutes say little about anyone,
	// so the minutes check is skipped. It also covers preseason, where the
	// bootstrap still carries last season's totals.
	minGameweeksForMinutes = 3
	// A player counts as running hot when goals + assists beat their expected
	// figure by at least overperformAbs *and* by at least overperformRatio
	// times. Both must hold so a big-minutes striker a couple of goals up on
	// xG, or a low-xG player 1 to 0, is not flagged as noise.
	overperformAbs   = 3.0
	overperformRatio = 1.5
)

const bestNoteText = "best marks the option with the highest net projected points, not the lowest risk. " +
	"confidence rates how far each option's incoming players can be trusted (share of available minutes played, and goal involvements versus expected), " +
	"and safest marks the most reliable option."

// confidenceRank orders levels for comparison.
func confidenceRank(level string) int {
	switch level {
	case ConfidenceHigh:
		return 2
	case ConfidenceMedium:
		return 1
	default:
		return 0
	}
}

func lowerConfidence(level string) string {
	if level == ConfidenceHigh {
		return ConfidenceMedium
	}
	return ConfidenceLow
}

// gameweeksPlayed counts finished gameweeks.
func gameweeksPlayed(b *fpl.Bootstrap) int {
	n := 0
	for _, e := range b.Events {
		if e.Finished {
			n++
		}
	}
	return n
}

// assessIncomingPlayer rates how far a player's projection can be trusted,
// with a note when it is below high. gameweeks is the number of finished
// gameweeks, so 90*gameweeks minutes were available.
//
// Two things weaken a projection: a small sample (a small share of the
// available minutes, so form and points-per-game rest on a handful of
// matches), and results running well ahead of the underlying chances (goals
// and assists far above xG + xA, which tends to regress). The second drops the
// rating one level. The first applies only once minGameweeksForMinutes
// gameweeks are finished.
func assessIncomingPlayer(p *fpl.Player, gameweeks int) (string, string) {
	level := ConfidenceHigh
	var notes []string

	if gameweeks >= minGameweeksForMinutes {
		share := float64(p.Minutes) / float64(90*gameweeks)
		switch {
		case share < lowMinutesShare:
			level = ConfidenceLow
		case share < mediumMinutesShare:
			level = ConfidenceMedium
		}
		if level != ConfidenceHigh {
			notes = append(notes, fmt.Sprintf("only %d of %d possible minutes this season", p.Minutes, 90*gameweeks))
		}
	}

	actual := float64(p.GoalsScored + p.Assists)
	expected := p.ExpectedGoals.Float() + p.ExpectedAssists.Float()
	if actual-expected >= overperformAbs && actual >= overperformRatio*expected {
		level = lowerConfidence(level)
		notes = append(notes, fmt.Sprintf("%d goal involvements from %.1f expected, which tends to regress", int(actual), expected))
	}

	if len(notes) == 0 {
		return level, ""
	}
	return level, p.WebName + ": " + strings.Join(notes, "; ")
}

// summarizeOptionConfidence rates a transfer option by its weakest incoming
// player and returns a note for each player rated below high. An option with
// no incoming players (keeping the squad as is) is high confidence.
func summarizeOptionConfidence(incoming []*fpl.Player, gameweeks int) (string, []string) {
	level := ConfidenceHigh
	notes := []string{}
	for _, p := range incoming {
		if p == nil {
			continue // an id missing from the bootstrap; nothing to assess
		}
		l, note := assessIncomingPlayer(p, gameweeks)
		if confidenceRank(l) < confidenceRank(level) {
			level = l
		}
		if note != "" {
			notes = append(notes, note)
		}
	}
	return level, notes
}

// markSafest flags the most reliable option: highest confidence, then highest
// net projected points, then the fewest transfers.
//
// The last tie-break relies on options arriving in ascending order of
// transfers, as the sweep produces them, and on the comparisons being strict:
// an equal option never displaces an earlier one. Changing either breaks it.
func markSafest(options []TransferPlanOption) {
	safest := -1
	for i, opt := range options {
		switch {
		case safest == -1:
			safest = i
		case confidenceRank(opt.Confidence) > confidenceRank(options[safest].Confidence):
			safest = i
		case confidenceRank(opt.Confidence) == confidenceRank(options[safest].Confidence) &&
			opt.NetProjectedPoints > options[safest].NetProjectedPoints:
			safest = i
		}
	}
	if safest >= 0 {
		options[safest].Safest = true
	}
}

// optimalTransfersBudgetTenths returns the manager's total squad budget
// (squad value + bank, in tenths) and a note describing how it was derived.
//
// With selling prices from the transfer history, the budget is bank plus
// what FPL would pay for the squad, which is exact. Without them, FPL's own
// history.Current entries report total team value at market prices as of
// the latest recorded gameweek; and before a manager's first recorded
// gameweek (e.g. a brand-new preseason entry) this falls back to summing
// current prices, the same estimate transfers.go uses and caveats.
func optimalTransfersBudgetTenths(history *fpl.TeamHistory, squad []fpl.Player, bankTenths int, selling map[int]SquadPrice) (int, string) {
	if selling != nil {
		return bankTenths + sellingValueTenths(selling), sellingPriceNote + " Kept players count at their selling price in total_cost_m, and incoming players at their market price."
	}
	if history != nil && len(history.Current) > 0 {
		latest := history.Current[len(history.Current)-1]
		return latest.Value, "Budget is FPL's own reported total team value (squad + bank) as of your most recent recorded gameweek."
	}
	total := bankTenths
	for _, p := range squad {
		total += p.NowCost
	}
	return total, "Budget estimates use current player prices. FPL's selling price may differ if a player's value has risen since purchase — check the FPL app for your exact budget."
}

// OptimalTransfers finds the projected-points-maximizing set of transfers
// for a manager's current squad, sweeping MaxChanges from their free
// transfers up through a small hit-cost range (only when allowHits is set)
// so every option's points-vs-hit-cost tradeoff is visible.
//
// The return value is either an *OptimalTransfersResult on success or a
// *TransferError when the team's picks can't be fetched — the same
// non-exceptional-result convention TransferSuggestions uses.
func (e *Engine) OptimalTransfers(ctx context.Context, teamID int, gameweek *int, allowHits bool) (any, error) {
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	fixtures, err := e.client.Fixtures(ctx)
	if err != nil {
		return nil, err
	}

	currentGW := bootstrap.CurrentGameweek()
	nextGW := bootstrap.NextGameweek()

	// Same two-attempt lookup TransferSuggestions uses: nextGW's picks
	// reflect any changes the manager has already queued for the upcoming
	// deadline, falling back to the current gameweek's picks otherwise.
	picks, pErr := e.client.TeamPicks(ctx, teamID, nextGW)
	if pErr != nil {
		picks, pErr = e.client.TeamPicks(ctx, teamID, currentGW)
		if pErr != nil {
			return &TransferError{
				Error: fmt.Sprintf("Could not fetch picks for team %d. Check the team ID is correct.", teamID),
			}, nil
		}
	}

	mgrStatus, err := e.client.ManagerStatus(ctx, teamID, bootstrap)
	if err != nil {
		return nil, err
	}
	history, err := e.client.TeamHistory(ctx, teamID)
	if err != nil {
		return nil, err
	}

	byID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		byID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)

	lockedIDs := make([]int, 0, len(picks.Picks))
	lockedSet := make(map[int]bool, len(picks.Picks))
	squad := make([]fpl.Player, 0, len(picks.Picks))
	for _, pick := range picks.Picks {
		if p := byID[pick.Element]; p != nil {
			lockedIDs = append(lockedIDs, p.ID)
			lockedSet[p.ID] = true
			squad = append(squad, *p)
		}
	}

	gw := nextGW
	if gameweek != nil {
		gw = *gameweek
	}
	window := buildProjectionWindow(fixtures, gw, xpHorizonGWs)

	squadPtrs := make([]*fpl.Player, 0, len(lockedIDs))
	for _, id := range lockedIDs {
		squadPtrs = append(squadPtrs, byID[id])
	}
	selling := e.squadSellingPrices(ctx, teamID, squadPtrs)
	budgetTenths, budgetNote := optimalTransfersBudgetTenths(history, squad, RoundToInt(mgrStatus.Bank*10), selling)
	candidates := buildCandidates(bootstrap.Elements, window, nil, lockedSet)
	priceAtSelling(candidates, selling)
	priceRisks := e.priceRiskByPlayer(ctx)

	ceilings := []int{mgrStatus.FreeTransfers}
	if allowHits {
		for extra := 1; extra <= maxHitsConsidered; extra++ {
			ceilings = append(ceilings, mgrStatus.FreeTransfers+extra)
		}
	}

	played := gameweeksPlayed(bootstrap)

	results, err := solveTransferSweep(candidates, budgetTenths, lockedIDs, ceilings)
	if err != nil {
		return nil, err
	}

	options := make([]TransferPlanOption, 0, len(ceilings))
	bestIdx := -1
	partial := false
	for i, result := range results {
		partial = partial || !result.Optimal

		resultSet := make(map[int]bool, len(result.Squad))
		totalCost := 0
		for _, c := range result.Squad {
			resultSet[c.ID] = true
			totalCost += c.PriceTenths
		}

		var transfersOut, transfersIn []OptimalSquadSlot
		for _, id := range lockedIDs {
			if !resultSet[id] {
				p := byID[id]
				slot := slotOf(p, teams, p.NowCost, projectExpectedPoints(p, window[p.Team]))
				if sp, ok := selling[id]; ok {
					slot.SellingPriceM = float64(sp.SellingTenths) / 10
				}
				slot.PriceRisk = priceRiskFor(p, priceRisks)
				transfersOut = append(transfersOut, slot)
			}
		}
		for _, c := range result.Squad {
			if !lockedSet[c.ID] {
				slot := slotOf(byID[c.ID], teams, c.PriceTenths, c.Value)
				slot.PriceRisk = priceRiskFor(byID[c.ID], priceRisks)
				transfersIn = append(transfersIn, slot)
			}
		}

		numTransfers := len(transfersIn)
		paidTransfers := max(0, numTransfers-mgrStatus.FreeTransfers)
		optionHitCost := hitCost * paidTransfers
		netValue := Round(result.Value+float64(optionHitCost), 2)

		incoming := make([]*fpl.Player, 0, len(transfersIn))
		for _, in := range transfersIn {
			incoming = append(incoming, byID[in.ID])
		}
		confidence, confidenceNotes := summarizeOptionConfidence(incoming, played)

		options = append(options, TransferPlanOption{
			Confidence:         confidence,
			ConfidenceNotes:    confidenceNotes,
			NumTransfers:       numTransfers,
			HitCost:            optionHitCost,
			ProjectedPoints:    Round(result.Value, 2),
			NetProjectedPoints: netValue,
			Optimal:            result.Optimal,
			TotalCostM:         float64(totalCost) / 10,
			TransfersOut:       transfersOut,
			TransfersIn:        transfersIn,
		})
		if bestIdx == -1 || netValue > options[bestIdx].NetProjectedPoints {
			bestIdx = i
		}
	}
	if bestIdx >= 0 {
		options[bestIdx].Best = true
	}
	markSafest(options)

	priceRiskNote := ""
	for _, opt := range options {
		if anyPriceRisk(opt.TransfersOut) || anyPriceRisk(opt.TransfersIn) {
			priceRiskNote = priceRiskNoteText
		}
	}

	return &OptimalTransfersResult{
		PriceRiskNote: priceRiskNote,
		TeamID:        teamID,
		Gameweek:      gw,
		FreeTransfers: mgrStatus.FreeTransfers,
		BudgetM:       float64(budgetTenths) / 10,
		BudgetNote:    budgetNote,
		PoolNote: fmt.Sprintf(
			"Considered the top %d/%d/%d/%d GKP/DEF/MID/FWD candidates by projected points, plus every player already in your squad — a near-optimal, not certified-optimal-over-every-player, approximation needed to keep the search tractable.",
			candidatePoolCap[1], candidatePoolCap[2], candidatePoolCap[3], candidatePoolCap[4]),
		ProjectedPointsBasis: projectedPointsBasis(gw),
		BestNote:             bestNoteText,
		Options:              options,
		Partial:              partial,
		PartialNote:          partialNoteFor(partial),
	}, nil
}

// partialNoteText explains partial when a search hit its time limit.
const partialNoteText = "At least one option's search reached its time limit before proving it had the best squad, so that option (optimal: false) is the best found in time and a better plan may exist. Options are still valid squads."

func partialNoteFor(partial bool) string {
	if partial {
		return partialNoteText
	}
	return ""
}

// solveTransferSweep runs one Solve per MaxChanges ceiling, all at once.
//
// Each Solve is single-threaded and capped by optimalSquadTimeLimit, so
// running them in parallel bounds the sweep's wall time by one time limit
// rather than one per ceiling: measured live against five top-ranked teams,
// the sequential sweep took 6s to 27s, almost all of it in the two or three
// searches that ran to the 8s limit.
//
// Ceilings must ascend. A squad that fits ceiling k also fits every larger
// ceiling, so when a time-limited search at a larger ceiling ends below the
// one before it, the earlier squad is carried forward. The carried result
// keeps Optimal false, since neither search proved anything about the
// larger ceiling.
func solveTransferSweep(candidates []Candidate, budgetTenths int, lockedIDs []int, ceilings []int) ([]Result, error) {
	results := make([]Result, len(ceilings))
	var g errgroup.Group
	for i, ceiling := range ceilings {
		g.Go(func() error {
			r, err := Solve(candidates, SquadConstraints{
				BudgetTenths:  budgetTenths,
				PositionQuota: fplQuota,
				MaxPerClub:    3,
				Locked:        lockedIDs,
				MaxChanges:    ceiling,
				TimeLimit:     optimalSquadTimeLimit,
				Lineup:        &fplLineup,
			})
			results[i] = r
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	for i := 1; i < len(results); i++ {
		if results[i].Value < results[i-1].Value {
			results[i] = Result{Squad: results[i-1].Squad, Value: results[i-1].Value, Optimal: false}
		}
	}
	return results, nil
}

func slotOf(p *fpl.Player, teams map[int]*fpl.Team, priceTenths int, value float64) OptimalSquadSlot {
	return OptimalSquadSlot{
		ID:              p.ID,
		Name:            p.WebName,
		Team:            shortName(teams[p.Team]),
		Position:        Position(p.ElementType),
		CostM:           float64(priceTenths) / 10,
		ProjectedPoints: Round(value, 2),
	}
}
