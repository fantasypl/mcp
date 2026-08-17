package algo

import (
	"context"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/insights"
)

// stubFinishingLuckSource returns a fixed FinishingDelta map without any
// network access, matching stubIntelFetcher's pattern in chips_test.go.
type stubFinishingLuckSource struct {
	luck map[int]insights.FinishingDelta
	err  error
}

func (s *stubFinishingLuckSource) FinishingLuck(context.Context, string, int, int) (map[int]insights.FinishingDelta, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.luck, nil
}

// finishingRegressionFixture builds a minimal two-player bootstrap with the
// current gameweek set past GW1, so finishingLuckMap's no-look-ahead guard
// (throughGW = CurrentGameweek()-1) doesn't skip the fetch — unlike the
// shared golden fixtures, which are frozen at the season's opening
// gameweek and so never exercise this path (see harness_test.go).
func finishingRegressionFixture() (*fpl.Bootstrap, []fpl.Fixture) {
	teamID := 1
	bootstrap := &fpl.Bootstrap{
		Elements: []fpl.Player{
			{ID: 100, Code: 100, Team: teamID, ElementType: 4, WebName: "Underperformer", Status: "a", SelectedByPercent: 1.0, Form: 5.0, PointsPerGame: 4.0},
			{ID: 200, Code: 200, Team: teamID, ElementType: 4, WebName: "NoSignal", Status: "a", SelectedByPercent: 1.0, Form: 5.0, PointsPerGame: 4.0},
		},
		Teams: []fpl.Team{
			{ID: teamID, Code: 1, Name: "Team A", ShortName: "TMA"},
			{ID: 2, Code: 2, Name: "Team B", ShortName: "TMB"},
		},
		Events: []fpl.Event{
			{ID: 9, Finished: true},
			{ID: 10, IsCurrent: true},
			{ID: 11, IsNext: true},
		},
	}
	gw := 11 // matches the Events' IsNext gameweek, which Differentials defaults to
	fixtures := []fpl.Fixture{
		{ID: 1, Event: &gw, TeamH: teamID, TeamA: 2, TeamHDifficulty: 3, TeamADifficulty: 3},
	}
	return bootstrap, fixtures
}

func TestDifferentialsSurfacesFinishingRegressionWhenConfigured(t *testing.T) {
	bootstrap, fixtures := finishingRegressionFixture()
	e := NewEngine(NewStubClient(bootstrap, fixtures))
	e.Now = func() time.Time { return time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) }
	e.FinishingLuckSource = &stubFinishingLuckSource{luck: map[int]insights.FinishingDelta{
		100: {PlayerID: 100, Name: "Underperformer", ActualGoals: 1, SumXGOT: 4.0, ShotsOnTarget: 6},
		// 200 deliberately has no entry — should surface as no signal, not an error.
	}}

	got, err := e.Differentials(context.Background(), 100.0, ptr(11), 10)
	if err != nil {
		t.Fatalf("Differentials: %v", err)
	}

	byID := make(map[int]Differential, len(got.Differentials))
	for _, d := range got.Differentials {
		byID[d.Player.ID] = d
	}

	under := byID[100]
	if under.FinishingRegression == nil {
		t.Fatal("player 100 should have a FinishingRegression signal")
	}
	if under.FinishingRegression.Signal != "buy" {
		t.Errorf("signal = %q, want %q (1 goal from 4.0 xGOT is well underperforming)", under.FinishingRegression.Signal, "buy")
	}
	if under.FinishingRegression.Delta != -3.0 {
		t.Errorf("delta = %v, want -3.0", under.FinishingRegression.Delta)
	}
	if under.Why == "" {
		t.Fatal("Why should be non-empty")
	}

	noSignal := byID[200]
	if noSignal.FinishingRegression != nil {
		t.Errorf("player 200 has no shots data — FinishingRegression should be nil, got %+v", noSignal.FinishingRegression)
	}
}

func TestDifferentialsToleratesFinishingLuckSourceFailure(t *testing.T) {
	bootstrap, fixtures := finishingRegressionFixture()
	e := NewEngine(NewStubClient(bootstrap, fixtures))
	e.Now = func() time.Time { return time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) }
	e.FinishingLuckSource = &stubFinishingLuckSource{err: context.DeadlineExceeded}

	got, err := e.Differentials(context.Background(), 100.0, ptr(11), 10)
	if err != nil {
		t.Fatalf("Differentials should not fail when the enrichment source errors: %v", err)
	}
	for _, d := range got.Differentials {
		if d.FinishingRegression != nil {
			t.Errorf("%s: FinishingRegression should be nil when the source errored, got %+v", d.Player.Name, d.FinishingRegression)
		}
	}
}

func TestDifferentialsSkipsFinishingLuckSourceByDefault(t *testing.T) {
	bootstrap, fixtures := finishingRegressionFixture()
	e := NewEngine(NewStubClient(bootstrap, fixtures)) // FinishingLuckSource left nil, as NewEngine leaves it

	got, err := e.Differentials(context.Background(), 100.0, ptr(11), 10)
	if err != nil {
		t.Fatalf("Differentials: %v", err)
	}
	for _, d := range got.Differentials {
		if d.FinishingRegression != nil {
			t.Errorf("%s: FinishingRegression should be nil with no source configured, got %+v", d.Player.Name, d.FinishingRegression)
		}
	}
}
