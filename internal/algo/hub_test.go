package algo

import (
	"context"
	"strings"
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

// Issue #8: the hub shows both captain_score and FPL's own ep_next. When they
// disagree on the best captain among the starters, say so and say why.
func TestCaptainSignalNote(t *testing.T) {
	// The issue's squad, GW5.
	squad := []HubSquadEntry{
		{Slot: 1, Starter: true, Name: "Gibbs-White", CaptainScore: 12.93, EPNext: 6.5},
		{Slot: 2, Starter: true, Name: "Joao Pedro", CaptainScore: 10.30, EPNext: 8.2},
		{Slot: 3, Starter: true, Name: "Saka", CaptainScore: 11.50, EPNext: 7.5},
	}
	note := captainSignalNote(squad)
	for _, want := range []string{"Gibbs-White", "12.9", "Joao Pedro", "8.2", "captain_score", "ep_next", "fixture"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q is missing %q", note, want)
		}
	}

	t.Run("agreement gives no note", func(t *testing.T) {
		agree := []HubSquadEntry{
			{Starter: true, Name: "A", CaptainScore: 12, EPNext: 8},
			{Starter: true, Name: "B", CaptainScore: 10, EPNext: 6},
		}
		if got := captainSignalNote(agree); got != "" {
			t.Errorf("got %q, want no note", got)
		}
	})

	t.Run("a tie on ep_next is not a disagreement", func(t *testing.T) {
		tied := []HubSquadEntry{
			{Starter: true, Name: "A", CaptainScore: 12, EPNext: 6},
			{Starter: true, Name: "B", CaptainScore: 10, EPNext: 6},
		}
		if got := captainSignalNote(tied); got != "" {
			t.Errorf("got %q, want no note", got)
		}
	})

	t.Run("bench players are not captain candidates", func(t *testing.T) {
		benchLeader := []HubSquadEntry{
			{Starter: true, Name: "A", CaptainScore: 10, EPNext: 6},
			{Starter: false, Slot: 13, Name: "BenchStar", CaptainScore: 15, EPNext: 9},
		}
		if got := captainSignalNote(benchLeader); got != "" {
			t.Errorf("got %q, want no note: a benched player can't be captain", got)
		}
	})

	t.Run("all-zero ep_next (preseason) gives no note", func(t *testing.T) {
		zero := []HubSquadEntry{
			{Starter: true, Name: "A", CaptainScore: 12},
			{Starter: true, Name: "B", CaptainScore: 10},
		}
		if got := captainSignalNote(zero); got != "" {
			t.Errorf("got %q, want no note", got)
		}
	})
}

// End to end: the note reaches the hub result when the signals disagree.
func TestManagerHubCaptainSignalNoteWired(t *testing.T) {
	e := hubEngine(t)
	stub := e.client.(*StubClient)

	first, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	// Give ep_next 9 to the starter with the lowest captain_score and 0 to
	// everyone else, so ep_next must favour a different player.
	var lowest *HubSquadEntry
	for i := range first.Squad {
		s := &first.Squad[i]
		if s.Starter && (lowest == nil || s.CaptainScore < lowest.CaptainScore) {
			lowest = s
		}
	}
	for i := range stub.bootstrap.Elements {
		stub.bootstrap.Elements[i].EPNext = 0
		if stub.bootstrap.Elements[i].ID == lowest.ElementID {
			stub.bootstrap.Elements[i].EPNext = 9
		}
	}

	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.CaptainSignalNote, lowest.Name) {
		t.Errorf("CaptainSignalNote = %q, want it to name %s", got.CaptainSignalNote, lowest.Name)
	}
}
