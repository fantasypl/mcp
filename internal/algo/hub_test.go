package algo

import (
	"context"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
)

// hubEngine builds an Engine over the midseason fixture (real form values,
// unlike preseason) with a team's picks and season history wired in, so
// ManagerHub's squad-value, squad-health, and season-summary paths all have
// something non-trivial to compute over.
func hubEngine(t *testing.T) *Engine {
	t.Helper()
	b := loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_midseason.json"))
	f := loadJSON[[]fpl.Fixture](t, testdataPath("fixtures.json"))
	c := NewStubClient(b, f)

	picks := loadJSON[*fpl.TeamPicks](t, testdataPath("picks_squad1.json"))
	c.SetTeamPicks(syntheticTeamID, 1, picks)
	c.SetHistory(syntheticTeamID, &fpl.TeamHistory{
		Current: []fpl.HistoryGameweek{
			{Event: 1, Points: 58, Value: 1005, Bank: 3},
		},
		Chips: []fpl.ChipUsage{{Name: "wildcard", Event: 1}},
	})

	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }
	return e
}

func TestManagerHub(t *testing.T) {
	e := hubEngine(t)
	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}

	if got.TeamID != syntheticTeamID {
		t.Errorf("team_id = %d, want %d", got.TeamID, syntheticTeamID)
	}
	if got.SquadSize != 15 {
		t.Errorf("squad_size = %d, want 15", got.SquadSize)
	}
	if !got.SquadValid {
		t.Errorf("squad_valid = false, want true (num_starters = %d)", got.NumStarters)
	}
	if got.NumStarters+got.NumBench != got.SquadSize {
		t.Errorf("num_starters(%d) + num_bench(%d) != squad_size(%d)", got.NumStarters, got.NumBench, got.SquadSize)
	}

	// value=1005 (100.5m) - bank=3 (0.3m) = 100.2m squad value.
	if got.Bank != 0.3 {
		t.Errorf("bank = %v, want 0.3", got.Bank)
	}
	if got.SquadValue != 100.2 {
		t.Errorf("squad_value = %v, want 100.2", got.SquadValue)
	}
	if got.TotalBudget != 100.5 {
		t.Errorf("total_budget = %v, want 100.5", got.TotalBudget)
	}

	if got.SeasonSummary.TotalPoints != 58 {
		t.Errorf("season_summary.total_points = %d, want 58", got.SeasonSummary.TotalPoints)
	}
	if got.SeasonSummary.GameweeksPlayed != 1 {
		t.Errorf("season_summary.gameweeks_played = %d, want 1", got.SeasonSummary.GameweeksPlayed)
	}
	if got.SeasonSummary.BestGameweek == nil || got.SeasonSummary.BestGameweek.Points != 58 {
		t.Errorf("season_summary.best_gameweek = %+v, want points 58", got.SeasonSummary.BestGameweek)
	}
	if len(got.SeasonSummary.ChipsUsed) != 1 || got.SeasonSummary.ChipsUsed[0].Chip != "wildcard" {
		t.Errorf("season_summary.chips_used = %+v, want one wildcard entry", got.SeasonSummary.ChipsUsed)
	}

	// Every list field must be non-nil (marshals to [], never null) even
	// when empty — this hub aggregates several algorithms' outputs and any
	// one of them returning a nil slice would leak through as null.
	nilChecks := map[string]bool{
		"squad":                          got.Squad == nil,
		"squad_health.injured":           got.SquadHealth.InjuredOrDoubtful == nil,
		"squad_health.poor_form":         got.SquadHealth.PoorFormStarters == nil,
		"squad_health.tough_fixtures":    got.SquadHealth.ToughFixturesThisGW == nil,
		"transfer_suggestions":           got.TransferSuggestions == nil,
		"differential_targets":           got.DifferentialTargets == nil,
		"price_drop_risks":               got.PriceDropRisks == nil,
		"manager_status.chips_remaining": got.ManagerStatus.ChipsRemaining == nil,
	}
	for field, isNil := range nilChecks {
		if isNil {
			t.Errorf("%s is nil, want non-nil (possibly empty) slice", field)
		}
	}

	if len(got.CaptainRecommendation) == 0 {
		t.Error("captain_recommendation is empty, want at least one pick")
	}
	if got.PoweredBy == "" || got.PoweredBy == "FPL Intelligence — pip install fpl-intelligence" {
		t.Errorf("powered_by = %q, want the Go repo reference, not the PyPI one", got.PoweredBy)
	}
}

// Issue #3: the hub reports the manager's own bench (slots 12-15, their real
// auto-sub priority) and separately suggests an order by projected points.
// This is the issue's squad: McGinn (2.8) should come ahead of Matheus N.
// (2.5), and the goalkeeper stays in slot 12 whatever its projection.
func TestSuggestBenchOrder(t *testing.T) {
	squad := []HubSquadEntry{
		{Slot: 1, Starter: true, ElementID: 1, Name: "Starter", Position: "GKP", EPNext: 4.0},
		{Slot: 12, ElementID: 20, Name: "Tzolakis", Position: "GKP", EPNext: 0.5},
		{Slot: 13, ElementID: 21, Name: "Matheus N.", Position: "DEF", EPNext: 2.5},
		{Slot: 14, ElementID: 22, Name: "McGinn", Position: "MID", EPNext: 2.8},
		{Slot: 15, ElementID: 23, Name: "Solanke", Position: "FWD", EPNext: 2.0},
	}

	got := suggestBenchOrder(squad)

	wantNames := []string{"Tzolakis", "McGinn", "Matheus N.", "Solanke"}
	if len(got) != len(wantNames) {
		t.Fatalf("suggested %d bench players, want %d: %+v", len(got), len(wantNames), got)
	}
	for i, want := range wantNames {
		if got[i].Name != want {
			t.Errorf("suggested slot %d = %s, want %s", 12+i, got[i].Name, want)
		}
		if got[i].Slot != 12+i {
			t.Errorf("%s: Slot = %d, want %d", got[i].Name, got[i].Slot, 12+i)
		}
	}
	// The manager's real slot is kept so the difference is visible.
	if got[1].CurrentSlot != 14 || got[2].CurrentSlot != 13 {
		t.Errorf("CurrentSlot for McGinn/Matheus = %d/%d, want 14/13", got[1].CurrentSlot, got[2].CurrentSlot)
	}
}

// Equal projections keep the manager's own relative order, so the suggestion
// never reshuffles players for no reason.
func TestSuggestBenchOrderTiesKeepManagerOrder(t *testing.T) {
	squad := []HubSquadEntry{
		{Slot: 12, ElementID: 1, Name: "GK", Position: "GKP", EPNext: 1},
		{Slot: 13, ElementID: 2, Name: "First", Position: "DEF", EPNext: 2.0},
		{Slot: 14, ElementID: 3, Name: "Second", Position: "MID", EPNext: 2.0},
		{Slot: 15, ElementID: 4, Name: "Third", Position: "FWD", EPNext: 2.0},
	}
	got := suggestBenchOrder(squad)
	for i, want := range []string{"GK", "First", "Second", "Third"} {
		if got[i].Name != want {
			t.Errorf("position %d = %s, want %s", i, got[i].Name, want)
		}
	}
}

// End to end: the suggestion is present, the squad list itself is untouched,
// and every entry now carries the ep_next the suggestion is based on.
func TestManagerHubSuggestedBenchOrder(t *testing.T) {
	e := hubEngine(t)
	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.SuggestedBenchOrder) != got.NumBench {
		t.Fatalf("suggested bench has %d players, want %d", len(got.SuggestedBenchOrder), got.NumBench)
	}
	if got.SuggestedBenchOrder[0].Position != "GKP" {
		t.Errorf("first bench slot = %s, want the goalkeeper", got.SuggestedBenchOrder[0].Position)
	}
	outfield := got.SuggestedBenchOrder[1:]
	for i := 1; i < len(outfield); i++ {
		if outfield[i].EPNext > outfield[i-1].EPNext {
			t.Errorf("outfield bench not descending by ep_next: %+v", outfield)
		}
	}

	// squad still lists the manager's own picks in slot order.
	for i, s := range got.Squad {
		if s.Slot != i+1 {
			t.Errorf("squad[%d].Slot = %d, want %d: squad must stay in the manager's order", i, s.Slot, i+1)
		}
	}
}
