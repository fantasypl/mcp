package algo

import (
	"context"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/insights"
)

// stubRoleChangeSource returns fixed baseline/recent PlayerPosition maps
// without any network access, matching stubFinishingLuckSource's pattern.
// roleChangeMap always calls AveragePositions with fromGW=1 for the
// baseline window and a later fromGW for the recent one, so the stub keys
// off that.
type stubRoleChangeSource struct {
	baseline, recent map[int]insights.PlayerPosition
	err              error
}

func (s *stubRoleChangeSource) AveragePositions(_ context.Context, _ string, fromGW, _ int) (map[int]insights.PlayerPosition, error) {
	if s.err != nil {
		return nil, s.err
	}
	if fromGW == 1 {
		return s.baseline, nil
	}
	return s.recent, nil
}

// roleChangeFixture builds a three-player bootstrap with the current
// gameweek set far enough into the season (22) for roleChangeMap's window
// arithmetic (a 10-gameweek recent window plus at least one baseline
// gameweek) to actually run, unlike the shared golden fixtures which are
// frozen at the season's opening gameweek.
func roleChangeFixture() (*fpl.Bootstrap, []fpl.Fixture) {
	teamID := 1
	bootstrap := &fpl.Bootstrap{
		Elements: []fpl.Player{
			{ID: 100, Code: 100, Team: teamID, ElementType: 3, WebName: "Advancer", Status: "a", SelectedByPercent: 1.0, Form: 5.0, PointsPerGame: 4.0},
			{ID: 200, Code: 200, Team: teamID, ElementType: 1, WebName: "Keeper", Status: "a", SelectedByPercent: 1.0, Form: 5.0, PointsPerGame: 4.0},
			{ID: 300, Code: 300, Team: teamID, ElementType: 3, WebName: "Steady", Status: "a", SelectedByPercent: 1.0, Form: 5.0, PointsPerGame: 4.0},
		},
		Teams: []fpl.Team{
			{ID: teamID, Code: 1, Name: "Team A", ShortName: "TMA"},
			{ID: 2, Code: 2, Name: "Team B", ShortName: "TMB"},
		},
		Events: []fpl.Event{
			{ID: 21, Finished: true},
			{ID: 22, IsCurrent: true},
			{ID: 23, IsNext: true},
		},
	}
	gw := 23
	fixtures := []fpl.Fixture{
		{ID: 1, Event: &gw, TeamH: teamID, TeamA: 2, TeamHDifficulty: 3, TeamADifficulty: 3},
	}
	return bootstrap, fixtures
}

func TestDifferentialsSurfacesRoleChangeWhenConfigured(t *testing.T) {
	bootstrap, fixtures := roleChangeFixture()
	e := NewEngine(NewStubClient(bootstrap, fixtures))
	e.Now = func() time.Time { return time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) }
	e.RoleChangeSource = &stubRoleChangeSource{
		baseline: map[int]insights.PlayerPosition{
			100: {PlayerID: 100, Name: "Advancer", Position: "M", SumX: 40.0 * 6, Matches: 6},
			200: {PlayerID: 200, Name: "Keeper", Position: "G", SumX: 10.0 * 6, Matches: 6},
			300: {PlayerID: 300, Name: "Steady", Position: "M", SumX: 40.0 * 6, Matches: 6},
		},
		recent: map[int]insights.PlayerPosition{
			// Advancer: baseline avgX 40 -> recent avgX 50, DeltaX 10 >= threshold.
			100: {PlayerID: 100, Name: "Advancer", Position: "M", SumX: 50.0 * 6, Matches: 6},
			// Keeper: same-shaped drift, but a goalkeeper — must be excluded.
			200: {PlayerID: 200, Name: "Keeper", Position: "G", SumX: 20.0 * 6, Matches: 6},
			// Steady: negligible drift — must not qualify.
			300: {PlayerID: 300, Name: "Steady", Position: "M", SumX: 41.0 * 6, Matches: 6},
		},
	}

	got, err := e.Differentials(context.Background(), 100.0, ptr(23), 10)
	if err != nil {
		t.Fatalf("Differentials: %v", err)
	}

	byID := make(map[int]Differential, len(got.Differentials))
	for _, d := range got.Differentials {
		byID[d.Player.ID] = d
	}

	adv := byID[100]
	if adv.RoleChange == nil {
		t.Fatal("Advancer should have a RoleChange signal")
	}
	if adv.RoleChange.DeltaX != 10.0 {
		t.Errorf("DeltaX = %v, want 10.0", adv.RoleChange.DeltaX)
	}
	if adv.Why == "" {
		t.Fatal("Why should be non-empty")
	}

	keeper := byID[200]
	if keeper.RoleChange != nil {
		t.Errorf("Keeper is a goalkeeper — RoleChange should be nil, got %+v", keeper.RoleChange)
	}

	steady := byID[300]
	if steady.RoleChange != nil {
		t.Errorf("Steady's drift is below threshold — RoleChange should be nil, got %+v", steady.RoleChange)
	}
}

func TestDifferentialsTogglesRoleChangeOnMatchCount(t *testing.T) {
	bootstrap, fixtures := roleChangeFixture()
	e := NewEngine(NewStubClient(bootstrap, fixtures))
	e.Now = func() time.Time { return time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) }
	e.RoleChangeSource = &stubRoleChangeSource{
		baseline: map[int]insights.PlayerPosition{
			// Only 2 matches in the baseline window — below roleChangeMinMatches.
			100: {PlayerID: 100, Name: "Advancer", Position: "M", SumX: 40.0 * 2, Matches: 2},
		},
		recent: map[int]insights.PlayerPosition{
			100: {PlayerID: 100, Name: "Advancer", Position: "M", SumX: 50.0 * 6, Matches: 6},
		},
	}

	got, err := e.Differentials(context.Background(), 100.0, ptr(23), 10)
	if err != nil {
		t.Fatalf("Differentials: %v", err)
	}
	for _, d := range got.Differentials {
		if d.Player.ID == 100 && d.RoleChange != nil {
			t.Errorf("too few baseline matches — RoleChange should be nil, got %+v", d.RoleChange)
		}
	}
}

func TestDifferentialsToleratesRoleChangeSourceFailure(t *testing.T) {
	bootstrap, fixtures := roleChangeFixture()
	e := NewEngine(NewStubClient(bootstrap, fixtures))
	e.Now = func() time.Time { return time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) }
	e.RoleChangeSource = &stubRoleChangeSource{err: context.DeadlineExceeded}

	got, err := e.Differentials(context.Background(), 100.0, ptr(23), 10)
	if err != nil {
		t.Fatalf("Differentials should not fail when the enrichment source errors: %v", err)
	}
	for _, d := range got.Differentials {
		if d.RoleChange != nil {
			t.Errorf("%s: RoleChange should be nil when the source errored, got %+v", d.Player.Name, d.RoleChange)
		}
	}
}

func TestDifferentialsSkipsRoleChangeSourceByDefault(t *testing.T) {
	bootstrap, fixtures := roleChangeFixture()
	e := NewEngine(NewStubClient(bootstrap, fixtures)) // RoleChangeSource left nil, as NewEngine leaves it

	got, err := e.Differentials(context.Background(), 100.0, ptr(23), 10)
	if err != nil {
		t.Fatalf("Differentials: %v", err)
	}
	for _, d := range got.Differentials {
		if d.RoleChange != nil {
			t.Errorf("%s: RoleChange should be nil with no source configured, got %+v", d.Player.Name, d.RoleChange)
		}
	}
}

// refusingRoleChangeSource fails the test if AveragePositions is called at
// all — for asserting roleChangeMap's early-return guard actually skips the
// fetch, rather than calling it and merely tolerating an error (which would
// look identical from Differentials' output alone).
type refusingRoleChangeSource struct{ t *testing.T }

func (r refusingRoleChangeSource) AveragePositions(context.Context, string, int, int) (map[int]insights.PlayerPosition, error) {
	r.t.Helper()
	r.t.Fatal("AveragePositions should not be called this early in the season")
	return nil, nil
}

// A gameweek too early in the season for a baseline window (CurrentGameweek
// < roleChangeRecentWindow+2) must skip the fetch entirely rather than
// calling AveragePositions with a nonsensical baseline range.
func TestDifferentialsSkipsRoleChangeTooEarlyInSeason(t *testing.T) {
	bootstrap, fixtures := finishingRegressionFixture() // CurrentGameweek 10 — too early for a 10-GW recent window plus baseline
	e := NewEngine(NewStubClient(bootstrap, fixtures))
	e.Now = func() time.Time { return time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) }
	e.RoleChangeSource = refusingRoleChangeSource{t: t}

	got, err := e.Differentials(context.Background(), 100.0, ptr(11), 10)
	if err != nil {
		t.Fatalf("Differentials: %v", err)
	}
	for _, d := range got.Differentials {
		if d.RoleChange != nil {
			t.Errorf("%s: RoleChange should be nil this early in the season, got %+v", d.Player.Name, d.RoleChange)
		}
	}
}
