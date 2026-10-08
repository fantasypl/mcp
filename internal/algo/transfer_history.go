package algo

import (
	"context"
	"fmt"
	"slices"

	"github.com/fantasypl/mcp/internal/fpl"
)

// TransferHistoryResult is manager_transfer_history's response: every
// transfer the manager has made this season, most recent first, and the
// purchase, market and selling price of each player in their current squad.
type TransferHistoryResult struct {
	TeamID       int                    `json:"team_id"`
	NumTransfers int                    `json:"num_transfers"`
	PriceNote    string                 `json:"price_note"`
	Transfers    []TransferHistoryEntry `json:"transfers"`

	// The current squad's prices are best-effort: they need this
	// gameweek's picks, which a brand-new entry may not have yet.
	CurrentSquad       []SquadPriceEntry `json:"current_squad"`
	BankM              float64           `json:"bank_m"`
	SquadSellingValueM float64           `json:"squad_selling_value_m"`
	TotalBudgetM       float64           `json:"total_budget_m"`
	SquadNote          string            `json:"squad_note,omitempty"`
}

// TransferHistoryEntry is one transfer. PurchasePriceM is what the manager
// paid for the incoming player; SoldForM is what FPL paid them for the
// outgoing one, which is that player's selling price at the time.
type TransferHistoryEntry struct {
	Gameweek       int         `json:"gameweek"`
	Time           string      `json:"time,omitempty"`
	Chip           string      `json:"chip,omitempty"` // wildcard or freehit, when one was active that gameweek
	PlayerIn       TransferLeg `json:"player_in"`
	PurchasePriceM float64     `json:"purchase_price_m"`
	PlayerOut      TransferLeg `json:"player_out"`
	SoldForM       float64     `json:"sold_for_m"`
}

// TransferLeg identifies a player in a transfer, with their market price
// today for comparison against the price at the time.
type TransferLeg struct {
	ID       int     `json:"id"`
	Name     string  `json:"name"`
	Team     string  `json:"team"`
	Position string  `json:"position"`
	NowCostM float64 `json:"now_cost_m"`
}

// SquadPriceEntry is one current squad player's prices.
type SquadPriceEntry struct {
	ID             int     `json:"id"`
	Name           string  `json:"name"`
	Position       string  `json:"position"`
	PurchasePriceM float64 `json:"purchase_price_m"`
	PurchaseSource string  `json:"purchase_source"` // transfer or season_start
	CostM          float64 `json:"cost_m"`          // market price
	SellingPriceM  float64 `json:"selling_price_m"`
}

const transferHistoryPriceNote = "purchase_price_m is what you paid. sold_for_m is what FPL paid you: the selling price at the time, which keeps only half of any rise since purchase, rounded down to 0.1m. now_cost_m and cost_m are today's market prices. selling_price_m is what FPL would pay you for the player today."

// TransferHistory lists a manager's transfers this season and prices their
// current squad. It returns a *TransferError when the transfer history
// cannot be fetched, the same convention TransferSuggestions uses.
func (e *Engine) TransferHistory(ctx context.Context, teamID int) (any, error) {
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	transfers, err := e.client.ManagerTransfers(ctx, teamID)
	if err != nil {
		return &TransferError{
			Error: fmt.Sprintf("Could not fetch transfer history for team %d. Check the team ID is correct.", teamID),
		}, nil
	}

	byID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		byID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)

	// Chips are context, not essential, so a failed history fetch just
	// leaves them out.
	chipByGW := map[int]string{}
	if history, err := e.client.TeamHistory(ctx, teamID); err == nil {
		for _, ch := range history.Chips {
			if ch.Name == "wildcard" || ch.Name == "freehit" {
				chipByGW[ch.Event] = ch.Name
			}
		}
	}

	sorted := slices.Clone(transfers)
	slices.SortStableFunc(sorted, func(a, b fpl.ManagerTransfer) int {
		switch {
		case a.Event != b.Event:
			return b.Event - a.Event
		case a.Time > b.Time:
			return -1
		case a.Time < b.Time:
			return 1
		default:
			return 0
		}
	})

	entries := make([]TransferHistoryEntry, 0, len(sorted))
	for _, t := range sorted {
		entries = append(entries, TransferHistoryEntry{
			Gameweek:       t.Event,
			Time:           t.Time,
			Chip:           chipByGW[t.Event],
			PlayerIn:       transferLegOf(t.ElementIn, byID, teams),
			PurchasePriceM: float64(t.ElementInCost) / 10,
			PlayerOut:      transferLegOf(t.ElementOut, byID, teams),
			SoldForM:       float64(t.ElementOutCost) / 10,
		})
	}

	result := &TransferHistoryResult{
		TeamID:       teamID,
		NumTransfers: len(entries),
		PriceNote:    transferHistoryPriceNote,
		Transfers:    entries,
		CurrentSquad: []SquadPriceEntry{},
	}

	picks, err := e.client.TeamPicks(ctx, teamID, bootstrap.CurrentGameweek())
	if err != nil {
		result.SquadNote = "Could not fetch this gameweek's picks, so current squad prices are not shown."
		return result, nil
	}
	squad := make([]*fpl.Player, 0, len(picks.Picks))
	for _, pick := range picks.Picks {
		if p := byID[pick.Element]; p != nil {
			squad = append(squad, p)
		}
	}
	prices := squadSellingPrices(squad, transfers)
	for _, p := range squad {
		sp := prices[p.ID]
		result.CurrentSquad = append(result.CurrentSquad, SquadPriceEntry{
			ID:             p.ID,
			Name:           p.WebName,
			Position:       Position(p.ElementType),
			PurchasePriceM: float64(sp.PurchaseTenths) / 10,
			PurchaseSource: sp.PurchaseSource,
			CostM:          float64(sp.NowTenths) / 10,
			SellingPriceM:  float64(sp.SellingTenths) / 10,
		})
	}
	sellingTenths := sellingValueTenths(prices)
	result.BankM = float64(picks.EntryHistory.Bank) / 10
	result.SquadSellingValueM = float64(sellingTenths) / 10
	result.TotalBudgetM = float64(picks.EntryHistory.Bank+sellingTenths) / 10
	return result, nil
}

func transferLegOf(id int, byID map[int]*fpl.Player, teams map[int]*fpl.Team) TransferLeg {
	p := byID[id]
	if p == nil {
		return TransferLeg{ID: id, Name: "?", Team: "?", Position: "?"}
	}
	return TransferLeg{
		ID:       p.ID,
		Name:     p.WebName,
		Team:     shortName(teams[p.Team]),
		Position: Position(p.ElementType),
		NowCostM: float64(p.NowCost) / 10,
	}
}
