package algo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/fplauth"
)

// fakeMyTeam is a logged-in FPL account with a fixed team.
type fakeMyTeam struct {
	entry     int
	team      *fpl.MyTeam
	latest    []fpl.ManagerTransfer
	err       error // returned by every call when set
	latestErr error
	calls     int
}

func (f *fakeMyTeam) MyEntry(context.Context) (int, error) {
	f.calls++
	return f.entry, f.err
}

func (f *fakeMyTeam) MyTeam(_ context.Context, entry int) (*fpl.MyTeam, error) {
	if f.err != nil {
		return nil, f.err
	}
	if entry != f.entry {
		return nil, &fpl.HTTPError{StatusCode: 403, URL: "stub"}
	}
	return f.team, nil
}

func (f *fakeMyTeam) TransfersLatest(context.Context, int) ([]fpl.ManagerTransfer, error) {
	if f.latestErr != nil {
		return nil, f.latestErr
	}
	return f.latest, f.err
}

// squad1MyTeam is the synthetic squad as FPL's my-team endpoint would show
// it, with every selling price one tenth below market so the test can tell
// FPL's figures from computed ones.
func squad1MyTeam(t *testing.T, e *Engine) *fpl.MyTeam {
	t.Helper()
	picks := loadJSON[*fpl.TeamPicks](t, testdataPath("picks_squad1.json"))
	b, err := e.client.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cost := map[int]int{}
	for _, p := range b.Elements {
		cost[p.ID] = p.NowCost
	}
	mt := &fpl.MyTeam{Transfers: fpl.MyTeamTransfers{Bank: 7, Value: 1000, Made: 2, Cost: 4, Status: "cost"}}
	// Listed in reverse slot order to check the report sorts them.
	for i := len(picks.Picks) - 1; i >= 0; i-- {
		pk := picks.Picks[i]
		mt.Picks = append(mt.Picks, fpl.MyTeamPick{
			Element: pk.Element, Position: pk.Position, Multiplier: pk.Multiplier,
			IsCaptain: pk.IsCaptain, IsViceCaptain: pk.IsViceCaptain,
			PurchasePrice: cost[pk.Element] - 3, SellingPrice: cost[pk.Element] - 1,
		})
	}
	return mt
}

func TestTransferHistoryUsesMyTeamPricesForOwnTeam(t *testing.T) {
	e := newEngineWithSquadAndHistory(t, "midseason")
	stubOf(t, e).SetTransfers(syntheticTeamID, squad1Transfers)
	e.MyTeamSource = &fakeMyTeam{entry: syntheticTeamID, team: squad1MyTeam(t, e)}

	got, err := e.TransferHistory(context.Background(), syntheticTeamID)
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*TransferHistoryResult)
	for _, s := range res.CurrentSquad {
		if s.PurchaseSource != PurchaseFromMyTeam {
			t.Errorf("%s purchase_source = %q, want %q", s.Name, s.PurchaseSource, PurchaseFromMyTeam)
		}
		if s.ID == 426 && (s.SellingPriceM != 11.9 || s.PurchasePriceM != 11.7 || s.CostM != 12.0) {
			t.Errorf("B.Fernandes selling/purchase/cost = %v/%v/%v, want FPL's 11.9/11.7/12.0", s.SellingPriceM, s.PurchasePriceM, s.CostM)
		}
	}
}

// Without a login, or with a login for another manager, or with a failing
// login, prices are computed from public data exactly as before.
func TestSellingPricesUnchangedWithoutUsableLogin(t *testing.T) {
	for name, src := range map[string]MyTeamSource{
		"no login":      nil,
		"other manager": &fakeMyTeam{entry: 12345, team: &fpl.MyTeam{}},
		"expired":       &fakeMyTeam{entry: syntheticTeamID, err: &fplauth.Error{Msg: fplauth.Expired}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEngineWithSquadAndHistory(t, "midseason")
			stubOf(t, e).SetTransfers(syntheticTeamID, squad1Transfers)
			e.MyTeamSource = src
			got, err := e.TransferHistory(context.Background(), syntheticTeamID)
			if err != nil {
				t.Fatal(err)
			}
			res := got.(*TransferHistoryResult)
			if res.SquadSellingValueM != float64(squad1SellingTenths)/10 {
				t.Errorf("squad_selling_value_m = %v, want the computed %v", res.SquadSellingValueM, float64(squad1SellingTenths)/10)
			}
			for _, s := range res.CurrentSquad {
				if s.PurchaseSource == PurchaseFromMyTeam {
					t.Errorf("%s uses FPL's prices without a usable login", s.Name)
				}
			}
		})
	}
}

// When the public transfer history fails, a login covering the whole squad
// still gives the hub selling prices.
func TestManagerHubUsesMyTeamPricesWithoutTransferHistory(t *testing.T) {
	e := hubEngine(t)
	mt := squad1MyTeam(t, e)
	e.MyTeamSource = &fakeMyTeam{entry: syntheticTeamID, team: mt}

	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, pk := range mt.Picks {
		want += pk.SellingPrice
	}
	if got.SquadSellingValue != float64(want)/10 || got.BudgetNote != sellingPriceNote {
		t.Errorf("squad_selling_value = %v, budget_note = %q; want %v and the selling-price note", got.SquadSellingValue, got.BudgetNote, float64(want)/10)
	}
}

// A partial my-team (the squad changed since the public picks) cannot stand
// in for a missing transfer history.
func TestPartialMyTeamDoesNotReplaceMissingHistory(t *testing.T) {
	e := hubEngine(t)
	mt := squad1MyTeam(t, e)
	mt.Picks = mt.Picks[:14]
	e.MyTeamSource = &fakeMyTeam{entry: syntheticTeamID, team: mt}
	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got.SquadSellingValue != 0 || got.BudgetNote != marketPriceNote {
		t.Errorf("squad_selling_value = %v, budget_note = %q; want the market-price fallback", got.SquadSellingValue, got.BudgetNote)
	}
}

func TestMyTeamWithoutLogin(t *testing.T) {
	got, err := newEngine(t, "midseason").MyTeam(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(map[string]any)
	if !ok || m["isError"] != true || !strings.Contains(m["error"].(string), "off by default") {
		t.Errorf("got %#v, want an error saying the login is optional and off", got)
	}
}

func TestMyTeamExpiredLogin(t *testing.T) {
	e := newEngine(t, "midseason")
	e.MyTeamSource = &fakeMyTeam{err: &fplauth.Error{Msg: fplauth.Expired}}
	got, err := e.MyTeam(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := got.(map[string]any); !ok || m["error"] != fplauth.Expired {
		t.Errorf("got %#v, want the expired-login message", got)
	}
}

func TestMyTeamOtherErrorsAreReturned(t *testing.T) {
	e := newEngine(t, "midseason")
	e.MyTeamSource = &fakeMyTeam{err: &fpl.HTTPError{StatusCode: 503, URL: "stub"}}
	if _, err := e.MyTeam(context.Background()); err == nil {
		t.Error("an outage should be returned as an error, not shown as a login problem")
	}
}

func TestMyTeamReport(t *testing.T) {
	e := newEngineWithSquadAndHistory(t, "midseason")
	mt := squad1MyTeam(t, e)
	mt.Transfers = fpl.MyTeamTransfers{Status: "unlimited", Limit: nil, Made: 3, Bank: 15, Value: 1012}
	mt.Chips = []fpl.MyTeamChip{
		{Name: "wildcard", StatusForEntry: "active", StartEvent: 1, StopEvent: 19},
		{Name: "bboost", StatusForEntry: "available", StartEvent: 1, StopEvent: 19},
	}
	e.MyTeamSource = &fakeMyTeam{entry: syntheticTeamID, team: mt, latest: []fpl.ManagerTransfer{
		{ElementIn: 426, ElementInCost: 120, ElementOut: 411, ElementOutCost: 150},
	}}

	got, err := e.MyTeam(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	res, ok := got.(*MyTeamResult)
	if !ok {
		t.Fatalf("got %#v, want *MyTeamResult", got)
	}
	if res.TeamID != syntheticTeamID || res.NumPicks != 15 {
		t.Errorf("team_id %d, num_picks %d; want %d and 15", res.TeamID, res.NumPicks, syntheticTeamID)
	}
	for i, p := range res.Squad {
		if p.Slot != i+1 || p.Starting != (i < 11) || p.Name == "" || p.Name == "?" {
			t.Errorf("squad[%d] = %+v, want slot %d in order with a name", i, p, i+1)
		}
	}
	sell := 0
	for _, pk := range mt.Picks {
		sell += pk.SellingPrice
	}
	if res.SquadSellingValueM != float64(sell)/10 || res.BankM != 1.5 || res.TotalBudgetM != float64(sell+15)/10 {
		t.Errorf("selling %v bank %v total %v", res.SquadSellingValueM, res.BankM, res.TotalBudgetM)
	}
	if !res.Transfers.Unlimited || res.Transfers.FreeTransfers != nil || res.ActiveChip != "wildcard" {
		t.Errorf("transfers %+v, active_chip %q; want unlimited on an active wildcard", res.Transfers, res.ActiveChip)
	}
	if len(res.PendingTransfers) != 1 || res.PendingTransfers[0].PlayerIn.Name != "B.Fernandes" || res.PendingTransfers[0].SoldForM != 15.0 {
		t.Errorf("pending = %+v", res.PendingTransfers)
	}
	if len(res.Chips) != 2 || res.Chips[0].PlayedInGameweeks == nil {
		t.Errorf("chips = %+v, want two with a non-nil played list", res.Chips)
	}
}

func TestMyTeamPendingListIsOptional(t *testing.T) {
	e := newEngineWithSquadAndHistory(t, "midseason")
	e.MyTeamSource = &fakeMyTeam{entry: syntheticTeamID, team: squad1MyTeam(t, e), latestErr: errors.New("boom")}
	got, err := e.MyTeam(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*MyTeamResult)
	if res.PendingNote == "" || res.PendingTransfers == nil || res.NumPicks != 15 {
		t.Errorf("got note %q, pending %v, picks %d; want a note, an empty list and the squad", res.PendingNote, res.PendingTransfers, res.NumPicks)
	}
}
