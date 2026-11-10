package algo

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/fplauth"
)

// MyTeamSource reads the logged-in manager's team (issue #26). It is nil
// unless the user sets up the optional FPL login, and every caller treats
// nil as "not logged in" and keeps its public-data behaviour.
type MyTeamSource interface {
	MyEntry(ctx context.Context) (int, error)
	MyTeam(ctx context.Context, entry int) (*fpl.MyTeam, error)
	TransfersLatest(ctx context.Context, entry int) ([]fpl.ManagerTransfer, error)
}

// PurchaseFromMyTeam marks a purchase and selling price read from FPL's
// logged-in my-team endpoint rather than computed from public data.
const PurchaseFromMyTeam = "fpl_my_team"

// myTeamPrices returns FPL's own purchase and selling prices for the squad
// when the login belongs to teamID, keyed by player ID. It returns nil when
// there is no login, the login is another manager's, or the fetch fails:
// callers then use the prices computed from public data.
func (e *Engine) myTeamPrices(ctx context.Context, teamID int, squad []*fpl.Player) map[int]SquadPrice {
	if e.MyTeamSource == nil {
		return nil
	}
	entry, err := e.MyTeamSource.MyEntry(ctx)
	if err != nil || entry != teamID {
		return nil
	}
	mt, err := e.MyTeamSource.MyTeam(ctx, entry)
	if err != nil {
		return nil
	}
	now := make(map[int]int, len(squad))
	for _, p := range squad {
		if p != nil {
			now[p.ID] = p.NowCost
		}
	}
	out := make(map[int]SquadPrice, len(mt.Picks))
	for _, pk := range mt.Picks {
		nowCost, ok := now[pk.Element]
		if !ok {
			continue
		}
		out[pk.Element] = SquadPrice{
			PurchaseTenths: pk.PurchasePrice,
			NowTenths:      nowCost,
			SellingTenths:  pk.SellingPrice,
			PurchaseSource: PurchaseFromMyTeam,
		}
	}
	return out
}

// overlayMyTeamPrices replaces computed prices with FPL's own where the
// login allows. computed may be nil (the public transfer history failed);
// the exact prices are then used only if they cover the whole squad, since
// callers read a nil map as "no prices" and a partial one as complete.
func (e *Engine) overlayMyTeamPrices(ctx context.Context, teamID int, squad []*fpl.Player, computed map[int]SquadPrice) map[int]SquadPrice {
	exact := e.myTeamPrices(ctx, teamID, squad)
	if len(exact) == 0 {
		return computed
	}
	if computed == nil {
		for _, p := range squad {
			if p == nil {
				continue
			}
			if _, ok := exact[p.ID]; !ok {
				return nil
			}
		}
		return exact
	}
	for id, sp := range exact {
		if _, ok := computed[id]; ok {
			computed[id] = sp
		}
	}
	return computed
}

// MyTeamResult is my_team's response: the logged-in manager's squad as it
// stands in the FPL app, before the deadline locks it in.
type MyTeamResult struct {
	TeamID      int    `json:"team_id"`
	ForGameweek int    `json:"for_gameweek"`
	Note        string `json:"note"`

	NumPicks int          `json:"num_picks"`
	Squad    []MyTeamPick `json:"squad"`

	BankM              float64 `json:"bank_m"`
	SquadValueM        float64 `json:"squad_value_m"`
	SquadSellingValueM float64 `json:"squad_selling_value_m"`
	TotalBudgetM       float64 `json:"total_budget_m"`

	Transfers        MyTeamTransferState `json:"transfers"`
	PendingTransfers []TransferLegPair   `json:"pending_transfers"`
	PendingNote      string              `json:"pending_transfers_note,omitempty"`

	Chips      []MyTeamChipState `json:"chips"`
	ActiveChip string            `json:"active_chip,omitempty"`
}

// MyTeamPick is one squad slot. Slots 1-11 start and 12-15 are the bench in
// order.
type MyTeamPick struct {
	Slot           int     `json:"slot"`
	Starting       bool    `json:"starting"`
	ID             int     `json:"id"`
	Name           string  `json:"name"`
	Team           string  `json:"team"`
	Position       string  `json:"position"`
	IsCaptain      bool    `json:"is_captain"`
	IsViceCaptain  bool    `json:"is_vice_captain"`
	Multiplier     int     `json:"multiplier"`
	PurchasePriceM float64 `json:"purchase_price_m"`
	SellingPriceM  float64 `json:"selling_price_m"`
	CostM          float64 `json:"cost_m"`
}

// MyTeamTransferState is the transfer budget for the next deadline.
// FreeTransfers is nil when transfers are unlimited (Wildcard or Free Hit).
type MyTeamTransferState struct {
	FreeTransfers  *int   `json:"free_transfers"`
	Unlimited      bool   `json:"unlimited"`
	Made           int    `json:"made"`
	PointsCost     int    `json:"points_cost"`
	NextTransferIs string `json:"next_transfer_is"`
}

// TransferLegPair is one pending transfer.
type TransferLegPair struct {
	PlayerIn       TransferLeg `json:"player_in"`
	PurchasePriceM float64     `json:"purchase_price_m"`
	PlayerOut      TransferLeg `json:"player_out"`
	SoldForM       float64     `json:"sold_for_m"`
}

// MyTeamChipState is one chip as FPL reports it for this manager.
type MyTeamChipState struct {
	Name                string `json:"name"`
	Status              string `json:"status"`
	PlayedInGameweeks   []int  `json:"played_in_gameweeks"`
	AvailableFromGW     int    `json:"available_from_gameweek"`
	AvailableToGameweek int    `json:"available_to_gameweek"`
}

const myTeamNote = "From your FPL login, so it shows the squad as it stands in the FPL app now: pending transfers, captain and chip choices are included before the deadline confirms them. Prices are FPL's own: purchase_price_m is what you paid, selling_price_m is what FPL pays you, cost_m is today's market price."

// MyTeam reports the logged-in manager's team. Errors that the user can fix
// (no login, expired login) come back as an error result with a message
// saying how; other errors are returned.
func (e *Engine) MyTeam(ctx context.Context) (any, error) {
	if e.MyTeamSource == nil {
		return myTeamError(fplauth.ErrNotConfigured), nil
	}
	entry, err := e.MyTeamSource.MyEntry(ctx)
	if err != nil {
		return loginErr(err)
	}
	mt, err := e.MyTeamSource.MyTeam(ctx, entry)
	if err != nil {
		return loginErr(err)
	}
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		byID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)

	picks := slices.Clone(mt.Picks)
	slices.SortFunc(picks, func(a, b fpl.MyTeamPick) int { return a.Position - b.Position })

	res := &MyTeamResult{
		TeamID:           entry,
		ForGameweek:      bootstrap.NextGameweek(),
		Note:             myTeamNote,
		Squad:            make([]MyTeamPick, 0, len(picks)),
		BankM:            float64(mt.Transfers.Bank) / 10,
		SquadValueM:      float64(mt.Transfers.Value) / 10,
		PendingTransfers: []TransferLegPair{},
		Chips:            make([]MyTeamChipState, 0, len(mt.Chips)),
	}
	selling := 0
	for _, pk := range picks {
		leg := transferLegOf(pk.Element, byID, teams)
		res.Squad = append(res.Squad, MyTeamPick{
			Slot:           pk.Position,
			Starting:       pk.Position <= 11,
			ID:             pk.Element,
			Name:           leg.Name,
			Team:           leg.Team,
			Position:       leg.Position,
			IsCaptain:      pk.IsCaptain,
			IsViceCaptain:  pk.IsViceCaptain,
			Multiplier:     pk.Multiplier,
			PurchasePriceM: float64(pk.PurchasePrice) / 10,
			SellingPriceM:  float64(pk.SellingPrice) / 10,
			CostM:          leg.NowCostM,
		})
		selling += pk.SellingPrice
	}
	res.NumPicks = len(res.Squad)
	res.SquadSellingValueM = float64(selling) / 10
	res.TotalBudgetM = float64(selling+mt.Transfers.Bank) / 10

	t := mt.Transfers
	res.Transfers = MyTeamTransferState{
		FreeTransfers:  t.Limit,
		Unlimited:      t.Limit == nil,
		Made:           t.Made,
		PointsCost:     t.Cost,
		NextTransferIs: t.Status,
	}

	for _, ch := range mt.Chips {
		played := ch.PlayedByEntry
		if played == nil {
			played = []int{}
		}
		res.Chips = append(res.Chips, MyTeamChipState{
			Name:                ch.Name,
			Status:              ch.StatusForEntry,
			PlayedInGameweeks:   played,
			AvailableFromGW:     ch.StartEvent,
			AvailableToGameweek: ch.StopEvent,
		})
		if ch.StatusForEntry == "active" {
			res.ActiveChip = ch.Name
		}
	}

	// Pending transfers are useful but not essential: the squad above
	// already has them applied.
	latest, err := e.MyTeamSource.TransfersLatest(ctx, entry)
	if err != nil {
		res.PendingNote = "Could not fetch the pending transfer list. The squad above already includes them."
	} else {
		for _, tr := range latest {
			res.PendingTransfers = append(res.PendingTransfers, TransferLegPair{
				PlayerIn:       transferLegOf(tr.ElementIn, byID, teams),
				PurchasePriceM: float64(tr.ElementInCost) / 10,
				PlayerOut:      transferLegOf(tr.ElementOut, byID, teams),
				SoldForM:       float64(tr.ElementOutCost) / 10,
			})
		}
	}
	return res, nil
}

// myTeamError is the error shape the MCP tools return.
func myTeamError(err error) map[string]any {
	return map[string]any{"isError": true, "error": err.Error()}
}

// loginErr turns a login problem the user can fix into an error result and
// passes anything else through.
func loginErr(err error) (any, error) {
	var ae *fplauth.Error
	if errors.As(err, &ae) || errors.Is(err, fplauth.ErrNotConfigured) {
		return myTeamError(err), nil
	}
	return nil, fmt.Errorf("my_team: %w", err)
}
