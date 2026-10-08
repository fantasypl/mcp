package algo

import (
	"context"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Issue #18: FPL pays the purchase price plus half of any rise, rounded down
// to 0.1m, and the market price after a fall. Prices are in tenths.
func TestSellingPrice(t *testing.T) {
	tests := []struct {
		name          string
		purchase, now int
		want          int
	}{
		{"rise of 0.4m keeps 0.2m", 50, 54, 52},
		{"odd rise of 0.5m rounds the profit down", 50, 55, 52},
		{"rise of 0.1m keeps nothing", 50, 51, 50},
		{"rise of 0.3m keeps 0.1m", 74, 77, 75},
		{"fall sells at market price", 50, 47, 47},
		{"unchanged", 50, 50, 50},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SellingPrice(tc.purchase, tc.now); got != tc.want {
				t.Errorf("SellingPrice(%d, %d) = %d, want %d", tc.purchase, tc.now, got, tc.want)
			}
		})
	}
}

func TestSquadSellingPrices(t *testing.T) {
	held := &fpl.Player{ID: 1, NowCost: 83, CostChangeStart: 3} // 8.0m at season start
	fell := &fpl.Player{ID: 2, NowCost: 68, CostChangeStart: -2}
	rose := &fpl.Player{ID: 3, NowCost: 63, CostChangeStart: 5}
	rebought := &fpl.Player{ID: 4, NowCost: 57, CostChangeStart: 7}

	transfers := []fpl.ManagerTransfer{
		// Most recent first, as FPL lists them.
		{Event: 6, ElementIn: 4, ElementInCost: 55, ElementOut: 9, Time: "2026-09-20T10:00:00Z"},
		{Event: 6, ElementIn: 9, ElementInCost: 50, ElementOut: 4, Time: "2026-09-20T09:00:00Z"},
		{Event: 3, ElementIn: 3, ElementInCost: 60, ElementOut: 8, Time: "2026-08-30T09:00:00Z"},
		{Event: 2, ElementIn: 4, ElementInCost: 50, ElementOut: 7, Time: "2026-08-23T09:00:00Z"},
		{Event: 2, ElementIn: 2, ElementInCost: 70, ElementOut: 6, Time: "2026-08-23T09:00:00Z"},
	}

	got := squadSellingPrices([]*fpl.Player{held, fell, rose, rebought}, transfers)

	want := map[int]SquadPrice{
		1: {PurchaseTenths: 80, NowTenths: 83, SellingTenths: 81, PurchaseSource: PurchaseFromSeasonStart},
		2: {PurchaseTenths: 70, NowTenths: 68, SellingTenths: 68, PurchaseSource: PurchaseFromTransfer},
		3: {PurchaseTenths: 60, NowTenths: 63, SellingTenths: 61, PurchaseSource: PurchaseFromTransfer},
		// Bought in GW2, sold and bought back in GW6: the later purchase counts.
		4: {PurchaseTenths: 55, NowTenths: 57, SellingTenths: 56, PurchaseSource: PurchaseFromTransfer},
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("player %d: got %+v, want %+v", id, got[id], w)
		}
	}
	if total := sellingValueTenths(got); total != 81+68+61+56 {
		t.Errorf("selling value = %d, want %d", total, 81+68+61+56)
	}
}

// Same-gameweek transfers of one player are ordered by time, not by list
// position, so a list in any order gives the latest purchase.
func TestLatestPurchasesOrdersByTime(t *testing.T) {
	transfers := []fpl.ManagerTransfer{
		{Event: 6, ElementIn: 4, ElementInCost: 50, Time: "2026-09-20T09:00:00Z"},
		{Event: 6, ElementIn: 4, ElementInCost: 55, Time: "2026-09-20T10:00:00Z"},
	}
	if got := latestPurchases(transfers)[4].ElementInCost; got != 55 {
		t.Errorf("latest purchase = %d, want 55", got)
	}
}

func TestPriceAtSelling(t *testing.T) {
	cands := []Candidate{{ID: 1, PriceTenths: 83}, {ID: 2, PriceTenths: 90}}
	priceAtSelling(cands, map[int]SquadPrice{1: {SellingTenths: 81}})
	if cands[0].PriceTenths != 81 || cands[1].PriceTenths != 90 {
		t.Errorf("got %+v, want squad player at 81 and others unchanged", cands)
	}
}

// squad1Transfers gives four of picks_squad1's players a purchase price
// through a transfer. Every squad player has cost_change_start 0 in the
// midseason fixture, so the rest sell at their market price.
//
//   - B.Fernandes (426): bought 11.5m, now 12.0m, sells 11.7m (odd rise)
//   - Isak (379): bought 8.6m, now 9.0m, sells 8.8m
//   - Gabriel (4): bought 8.2m, now 8.0m, sells 8.0m (fall)
//   - Rice (13): bought 7.4m, now 7.5m, sells 7.4m (0.1m rise)
//
// The squad is 93.0m at market prices and 92.4m at selling prices.
var squad1Transfers = []fpl.ManagerTransfer{
	{Event: 1, ElementIn: 426, ElementInCost: 115, ElementOut: 411, ElementOutCost: 150, Time: "2026-08-15T09:00:00Z"},
	{Event: 1, ElementIn: 379, ElementInCost: 86, ElementOut: 1, ElementOutCost: 60, Time: "2026-08-15T08:00:00Z"},
	{Event: 1, ElementIn: 4, ElementInCost: 82, ElementOut: 2, ElementOutCost: 50, Time: "2026-08-15T07:00:00Z"},
	{Event: 1, ElementIn: 13, ElementInCost: 74, ElementOut: 3, ElementOutCost: 50, Time: "2026-08-15T06:00:00Z"},
}

const squad1SellingTenths = 924

func stubOf(t *testing.T, e *Engine) *StubClient {
	t.Helper()
	c, ok := e.client.(*StubClient)
	if !ok {
		t.Fatalf("engine client is %T, want *StubClient", e.client)
	}
	return c
}

func TestManagerHubUsesSellingPrices(t *testing.T) {
	e := hubEngine(t)
	stubOf(t, e).SetTransfers(syntheticTeamID, squad1Transfers)

	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	// hubEngine's history has bank 0.3m.
	if got.SquadSellingValue != 92.4 {
		t.Errorf("squad_selling_value = %v, want 92.4", got.SquadSellingValue)
	}
	if got.TotalBudget != 92.7 {
		t.Errorf("total_budget = %v, want 92.7 (bank 0.3 + selling value 92.4)", got.TotalBudget)
	}
	if got.BudgetNote != sellingPriceNote {
		t.Errorf("budget_note = %q, want the selling-price note", got.BudgetNote)
	}
	for _, s := range got.Squad {
		if s.ElementID == 426 && (s.Cost != 12.0 || s.PurchasePrice != 11.5 || s.SellingPrice != 11.7) {
			t.Errorf("B.Fernandes cost/purchase/selling = %v/%v/%v, want 12.0/11.5/11.7", s.Cost, s.PurchasePrice, s.SellingPrice)
		}
	}
}

// Without a transfer history the hub keeps its market-price budget and says so.
func TestManagerHubFallsBackToMarketPrices(t *testing.T) {
	got, err := hubEngine(t).ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got.SquadSellingValue != 0 || got.BudgetNote != marketPriceNote {
		t.Errorf("squad_selling_value = %v, budget_note = %q; want 0 and the market-price note", got.SquadSellingValue, got.BudgetNote)
	}
}

func TestTransferSuggestionsUseSellingPrice(t *testing.T) {
	e := newEngineWithSquadAndHistory(t, "midseason")
	stubOf(t, e).SetTransfers(syntheticTeamID, squad1Transfers)

	got, err := e.TransferSuggestions(context.Background(), syntheticTeamID, 15, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := got.(*TransferSuggestionsResult)
	if !ok {
		t.Fatalf("got %T, want *TransferSuggestionsResult", got)
	}
	if res.BudgetNote != sellingPriceNote {
		t.Errorf("budget_note = %q, want the selling-price note", res.BudgetNote)
	}
	found := false
	for _, s := range res.TransferSuggestions {
		if s.TransferOut.ID != 426 {
			continue
		}
		found = true
		if s.TransferOut.Cost != 12.0 || s.TransferOut.SellingPrice != 11.7 {
			t.Errorf("cost/selling_price = %v/%v, want 12.0/11.7", s.TransferOut.Cost, s.TransferOut.SellingPrice)
		}
		if s.BudgetAvailable != 12.2 {
			t.Errorf("budget_available = %v, want 12.2 (selling 11.7 + bank 0.5)", s.BudgetAvailable)
		}
		for _, in := range s.TransferInOptions {
			if in.Cost > 12.2 {
				t.Errorf("replacement %s costs %v, over the 12.2 budget", in.Name, in.Cost)
			}
		}
	}
	if !found {
		t.Fatal("B.Fernandes not among the sell candidates")
	}
}

func TestOptimalTransfersUsesSellingPrices(t *testing.T) {
	e := newEngineWithSquadAndHistory(t, "midseason")
	stubOf(t, e).SetTransfers(syntheticTeamID, squad1Transfers)

	got, err := e.OptimalTransfers(context.Background(), syntheticTeamID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := got.(*OptimalTransfersResult)
	if !ok {
		t.Fatalf("got %T, want *OptimalTransfersResult", got)
	}
	// picks_squad1's bank is 0.5m.
	if want := float64(5+squad1SellingTenths) / 10; res.BudgetM != want {
		t.Errorf("budget_m = %v, want %v", res.BudgetM, want)
	}
	for _, opt := range res.Options {
		if opt.TotalCostM > res.BudgetM {
			t.Errorf("option with %d transfers costs %v, over the %v budget", opt.NumTransfers, opt.TotalCostM, res.BudgetM)
		}
		for _, out := range opt.TransfersOut {
			if out.SellingPriceM == 0 {
				t.Errorf("transfer out %s has no selling_price_m", out.Name)
			}
		}
	}
}

func TestOptimalTransfersBudgetTenths(t *testing.T) {
	history := &fpl.TeamHistory{Current: []fpl.HistoryGameweek{{Event: 5, Value: 1010, Bank: 5}}}
	squad := []fpl.Player{{ID: 1, NowCost: 100}}
	selling := map[int]SquadPrice{1: {SellingTenths: 97}, 2: {SellingTenths: 900}}

	if got, _ := optimalTransfersBudgetTenths(history, squad, 5, selling); got != 1002 {
		t.Errorf("with selling prices: got %d, want 1002 (bank 5 + 997)", got)
	}
	if got, _ := optimalTransfersBudgetTenths(history, squad, 5, nil); got != 1010 {
		t.Errorf("history fallback: got %d, want 1010", got)
	}
	if got, _ := optimalTransfersBudgetTenths(nil, squad, 5, nil); got != 105 {
		t.Errorf("market fallback: got %d, want 105", got)
	}
}

func TestAnalyzeHitForTeamUsesSellingPrice(t *testing.T) {
	b := loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_midseason.json"))
	for i := range b.Elements {
		if b.Elements[i].ID == 411 {
			// 12.4m: affordable at B.Fernandes' 12.0m market price plus the
			// 0.5m bank, but not at his 11.7m selling price.
			b.Elements[i].NowCost = 124
		}
	}
	c := NewStubClient(b, loadJSON[[]fpl.Fixture](t, testdataPath("fixtures.json")))
	c.SetTeamPicks(syntheticTeamID, 1, loadJSON[*fpl.TeamPicks](t, testdataPath("picks_squad1.json")))
	c.SetHistory(syntheticTeamID, loadJSON[*fpl.TeamHistory](t, testdataPath("history_squad1.json")))
	c.SetTransfers(syntheticTeamID, squad1Transfers)
	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }

	got, err := e.AnalyzeHitForTeam(context.Background(), syntheticTeamID, 426, 411, 5)
	if err != nil {
		t.Fatal(err)
	}
	bud := got.Budget
	if bud == nil || bud.Affordable == nil {
		t.Fatalf("budget = %+v, want an affordability verdict", bud)
	}
	if bud.PlayerOutCostM != 12.0 || bud.PlayerOutSellingPriceM != 11.7 || bud.AvailableM != 12.2 {
		t.Errorf("cost/selling/available = %v/%v/%v, want 12.0/11.7/12.2", bud.PlayerOutCostM, bud.PlayerOutSellingPriceM, bud.AvailableM)
	}
	if *bud.Affordable || bud.ShortfallM != 0.2 {
		t.Errorf("affordable = %v, shortfall = %v; want false and 0.2", *bud.Affordable, bud.ShortfallM)
	}
	if got.Analysis == nil {
		t.Error("the projection itself should still be present")
	}

	// A player outside the squad gets no verdict, with a note saying why.
	got, err = e.AnalyzeHitForTeam(context.Background(), syntheticTeamID, 411, 426, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got.Budget == nil || got.Budget.Affordable != nil || got.Budget.Note == "" {
		t.Errorf("budget = %+v, want no verdict and a note", got.Budget)
	}
}

func TestTransferHistory(t *testing.T) {
	e := newEngineWithSquadAndHistory(t, "midseason")
	c := stubOf(t, e)
	transfers := append([]fpl.ManagerTransfer{
		{Event: 2, ElementIn: 5, ElementInCost: 50, ElementOut: 38, ElementOutCost: 41, Time: "2026-08-22T09:00:00Z"},
	}, squad1Transfers...)
	c.SetTransfers(syntheticTeamID, transfers)

	got, err := e.TransferHistory(context.Background(), syntheticTeamID)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := got.(*TransferHistoryResult)
	if !ok {
		t.Fatalf("got %T, want *TransferHistoryResult", got)
	}
	if res.NumTransfers != 5 || len(res.Transfers) != 5 {
		t.Fatalf("num_transfers = %d, len = %d; want 5", res.NumTransfers, len(res.Transfers))
	}
	first := res.Transfers[0]
	if first.Gameweek != 2 || first.PlayerIn.ID != 5 || first.PurchasePriceM != 5.0 || first.PlayerOut.ID != 38 || first.SoldForM != 4.1 {
		t.Errorf("most recent transfer = %+v, want GW2 J.Timber in at 5.0, A.García out for 4.1", first)
	}
	if res.Transfers[1].PlayerIn.Name != "B.Fernandes" {
		t.Errorf("second transfer in = %q, want B.Fernandes (latest time within GW1)", res.Transfers[1].PlayerIn.Name)
	}
	if len(res.CurrentSquad) != 15 {
		t.Fatalf("current_squad has %d players, want 15", len(res.CurrentSquad))
	}
	for _, s := range res.CurrentSquad {
		if s.ID == 426 && (s.PurchasePriceM != 11.5 || s.CostM != 12.0 || s.SellingPriceM != 11.7 || s.PurchaseSource != PurchaseFromTransfer) {
			t.Errorf("B.Fernandes prices = %+v", s)
		}
	}
	// J.Timber (5) was bought at 5.0m and is now 6.5m, so sells for 5.7m:
	// 0.8m below market, on top of the other four players' 0.6m.
	wantSelling, wantTotal := float64(squad1SellingTenths-8)/10, float64(squad1SellingTenths-8+5)/10
	if res.SquadSellingValueM != wantSelling || res.BankM != 0.5 || res.TotalBudgetM != wantTotal {
		t.Errorf("selling value/bank/total = %v/%v/%v, want %v/0.5/%v", res.SquadSellingValueM, res.BankM, res.TotalBudgetM, wantSelling, wantTotal)
	}
}

func TestTransferHistoryUnknownTeam(t *testing.T) {
	got, err := newEngine(t, "midseason").TransferHistory(context.Background(), 12345)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(*TransferError); !ok {
		t.Errorf("got %T, want *TransferError", got)
	}
}
