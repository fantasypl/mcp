package algo

import (
	"context"

	"github.com/fantasypl/mcp/internal/fpl"
)

// FPL pays a manager their selling price for a player, not the market price.
// A player who has risen since purchase sells for the purchase price plus
// half the rise, rounded down to the nearest 0.1m; a player who has fallen
// sells for the market price. Prices here are in tenths (0.1m units), as FPL
// reports them, so the rounding is integer division.

// Purchase-price sources, reported per player so the output says how each
// figure was derived.
const (
	// PurchaseFromTransfer is the price the manager paid in their latest
	// transfer for this player.
	PurchaseFromTransfer = "transfer"
	// PurchaseFromSeasonStart is the season-start price (now_cost minus
	// cost_change_start), used for a player held since before any transfer.
	PurchaseFromSeasonStart = "season_start"
)

// sellingPriceNote explains the two price fields wherever both appear.
const (
	sellingPriceNote = "Budget uses selling prices: what FPL pays you for each player, which is the purchase price plus half of any rise since purchase, rounded down to 0.1m. Market prices are what a player costs to buy now."
	// marketPriceNote is the fallback when the transfer history is
	// unavailable. Its wording predates selling prices and is kept as is so
	// the golden fixtures, which have no transfer history, do not change.
	marketPriceNote = "Budget estimates use current player prices. FPL's selling price may differ if a player's value has risen since purchase — check the FPL app for your exact budget."
)

// SellingPrice applies FPL's half-profit rule. All values are in tenths.
func SellingPrice(purchase, now int) int {
	if now > purchase {
		return purchase + (now-purchase)/2
	}
	return now
}

// SquadPrice is one squad player's purchase, market and selling price, in
// tenths.
type SquadPrice struct {
	PurchaseTenths int
	NowTenths      int
	SellingTenths  int
	PurchaseSource string
}

// latestPurchases returns the price paid in the most recent transfer in of
// each player. FPL lists transfers most recent first, but this orders by
// gameweek and then time so it does not depend on that, and keeps the first
// listed entry on a full tie.
func latestPurchases(transfers []fpl.ManagerTransfer) map[int]fpl.ManagerTransfer {
	latest := make(map[int]fpl.ManagerTransfer, len(transfers))
	for _, t := range transfers {
		prev, ok := latest[t.ElementIn]
		if !ok || t.Event > prev.Event || (t.Event == prev.Event && t.Time > prev.Time) {
			latest[t.ElementIn] = t
		}
	}
	return latest
}

// squadSellingPrices computes each squad player's selling price from the
// manager's transfer history. A player with no transfer in was held since
// before the first transfer, so their purchase price is the season-start
// price.
//
// A manager who joined after gameweek 1 bought their first squad at the
// prices of that week, not at season-start prices, so for them the
// season-start fallback is an approximation.
func squadSellingPrices(squad []*fpl.Player, transfers []fpl.ManagerTransfer) map[int]SquadPrice {
	latest := latestPurchases(transfers)
	out := make(map[int]SquadPrice, len(squad))
	for _, p := range squad {
		if p == nil {
			continue
		}
		sp := SquadPrice{NowTenths: p.NowCost}
		if t, ok := latest[p.ID]; ok {
			sp.PurchaseTenths, sp.PurchaseSource = t.ElementInCost, PurchaseFromTransfer
		} else {
			sp.PurchaseTenths, sp.PurchaseSource = p.NowCost-p.CostChangeStart, PurchaseFromSeasonStart
		}
		sp.SellingTenths = SellingPrice(sp.PurchaseTenths, sp.NowTenths)
		out[p.ID] = sp
	}
	return out
}

// squadSellingPrices fetches the manager's transfer history and returns each
// squad player's prices. It returns nil when the history cannot be fetched,
// and callers then fall back to market prices with marketPriceNote.
func (e *Engine) squadSellingPrices(ctx context.Context, teamID int, squad []*fpl.Player) map[int]SquadPrice {
	transfers, err := e.client.ManagerTransfers(ctx, teamID)
	if err != nil {
		return nil
	}
	return squadSellingPrices(squad, transfers)
}

// sellingValueTenths sums the selling prices of the squad.
func sellingValueTenths(prices map[int]SquadPrice) int {
	total := 0
	for _, sp := range prices {
		total += sp.SellingTenths
	}
	return total
}

// priceAtSelling re-prices each squad player's candidate at their selling
// price. A kept player then counts against the budget at the same price the
// budget credits for them, so the solver's budget check is exactly "bank plus
// the selling prices of the players sold covers the players bought".
func priceAtSelling(cands []Candidate, prices map[int]SquadPrice) {
	for i := range cands {
		if sp, ok := prices[cands[i].ID]; ok {
			cands[i].PriceTenths = sp.SellingTenths
		}
	}
}
