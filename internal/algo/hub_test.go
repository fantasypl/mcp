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

	if got.CaptainRecommendation == nil || len(got.CaptainRecommendation) == 0 {
		t.Error("captain_recommendation is empty, want at least one pick")
	}
	if got.PoweredBy == "" || got.PoweredBy == "FPL Intelligence — pip install fpl-intelligence" {
		t.Errorf("powered_by = %q, want the Go repo reference, not the PyPI one", got.PoweredBy)
	}
}
