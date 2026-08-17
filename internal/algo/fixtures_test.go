package algo

import (
	"context"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/golden"
)

func TestFixtureOutlookMatchesGolden(t *testing.T) {
	bothFixtures(t, func(t *testing.T, e *Engine, suffix string) {
		cases := []struct {
			name     string
			ahead    int
			position string
			golden   string
		}{
			{"5 gameweeks", 5, "", "fixtures_5gw"},
			{"3 gameweeks, midfielders", 3, "MID", "fixtures_3gw_mid"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := e.FixtureOutlook(context.Background(), tc.ahead, tc.position)
				if err != nil {
					t.Fatal(err)
				}
				golden.Assert(t, goldenPath(tc.golden+suffix), got)
			})
		}
	})
}

// Every team must be ranked, even one with no fixtures in the window, which
// the contract assigns a neutral 3.0 difficulty rather than dropping.
func TestAllTeamsRanked(t *testing.T) {
	bothFixtures(t, func(t *testing.T, e *Engine, _ string) {
		res, err := e.FixtureOutlook(context.Background(), 5, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.TeamsByDifficulty) != 20 {
			t.Fatalf("ranked %d teams, want 20", len(res.TeamsByDifficulty))
		}
		for i, tm := range res.TeamsByDifficulty {
			if tm.Rank != i+1 {
				t.Errorf("rank %d at index %d", tm.Rank, i)
			}
			if i > 0 && tm.AdjustedDifficulty < res.TeamsByDifficulty[i-1].AdjustedDifficulty {
				t.Errorf("not sorted easiest-first at index %d", i)
			}
		}
	})
}

// The position filter must be applied, and returned back upper-cased.
func TestPositionFilter(t *testing.T) {
	bothFixtures(t, func(t *testing.T, e *Engine, _ string) {
		res, err := e.FixtureOutlook(context.Background(), 5, "mid")
		if err != nil {
			t.Fatal(err)
		}
		if res.PositionFilter == nil || *res.PositionFilter != "MID" {
			t.Fatalf("position_filter = %v, want MID", res.PositionFilter)
		}
		for _, p := range res.PlayersToTarget {
			if p.Position != "MID" {
				t.Errorf("%s is %s, expected only MID", p.Name, p.Position)
			}
		}
	})
}

// An unrecognised position is echoed back upper-cased but filters nothing,
// matching the contract `if position and position.upper() in POSITION_NAMES`.
func TestUnknownPositionDoesNotFilter(t *testing.T) {
	e := newEngine(t, "preseason")
	res, err := e.FixtureOutlook(context.Background(), 5, "wizard")
	if err != nil {
		t.Fatal(err)
	}
	if res.PositionFilter == nil || *res.PositionFilter != "WIZARD" {
		t.Fatalf("position_filter = %v, want WIZARD", res.PositionFilter)
	}
	seen := map[string]bool{}
	for _, p := range res.PlayersToTarget {
		seen[p.Position] = true
	}
	if len(seen) < 2 {
		t.Errorf("expected an unfiltered mix of positions, got %v", seen)
	}
}

// Injured players are excluded from the target list.
func TestInjuredPlayersExcluded(t *testing.T) {
	bothFixtures(t, func(t *testing.T, e *Engine, _ string) {
		bs, err := e.client.Bootstrap(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		status := map[string]string{}
		for i := range bs.Elements {
			status[bs.Elements[i].WebName] = bs.Elements[i].Status
		}
		res, _ := e.FixtureOutlook(context.Background(), 5, "")
		for _, p := range res.PlayersToTarget {
			if InjuryStatuses[status[p.Name]] {
				t.Errorf("%s has injury status %q but was suggested", p.Name, status[p.Name])
			}
		}
	})
}

// Preseason form is 0.0 for everyone, so the candidate sort is entirely ties.
// This documents that the golden file for that fixture is, in effect, a
// stability assertion.
func TestPreseasonTargetsAreAllTiedOnForm(t *testing.T) {
	e := newEngine(t, "preseason")
	res, err := e.FixtureOutlook(context.Background(), 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.PlayersToTarget) == 0 {
		t.Fatal("no target players")
	}
	for _, p := range res.PlayersToTarget {
		if p.Form != 0 {
			t.Skip("form is no longer uniformly zero; the season has started")
		}
	}
}

// stubCongestionSource returns a fixed team-code calendar without any
// network access, matching stubFinishingLuckSource's pattern.
type stubCongestionSource struct {
	calendar map[int][]time.Time
}

func (s *stubCongestionSource) TeamFixtureCalendar(context.Context, string, int, int) (map[int][]time.Time, error) {
	return s.calendar, nil
}

// congestionOutlookFixture builds a minimal two-team bootstrap with one
// fixture in the current gameweek, so FixtureOutlook's window covers it.
func congestionOutlookFixture(kickoff time.Time) (*fpl.Bootstrap, []fpl.Fixture) {
	bootstrap := &fpl.Bootstrap{
		Teams: []fpl.Team{
			{ID: 1, Code: 11, Name: "Congested FC", ShortName: "CFC"},
			{ID: 2, Code: 22, Name: "Fresh United", ShortName: "FRU"},
		},
		Events: []fpl.Event{{ID: 5, IsCurrent: true}},
	}
	gw := 5
	fixtures := []fpl.Fixture{
		{ID: 1, Event: &gw, TeamH: 1, TeamA: 2, TeamHDifficulty: 3, TeamADifficulty: 3, KickoffTime: kickoff.Format(time.RFC3339)},
	}
	return bootstrap, fixtures
}

func TestFixtureOutlookSurfacesCongestionWhenConfigured(t *testing.T) {
	kickoff := time.Date(2026, 1, 10, 15, 0, 0, 0, time.UTC)
	bootstrap, fixtures := congestionOutlookFixture(kickoff)
	e := NewEngine(NewStubClient(bootstrap, fixtures))
	e.Now = func() time.Time { return kickoff }
	e.CongestionSource = &stubCongestionSource{calendar: map[int][]time.Time{
		// Team code 11 (Congested FC) played 2 days before kickoff — congested.
		11: {kickoff.Add(-48 * time.Hour)},
		// Team code 22 (Fresh United) last played 10 days before — not congested.
		22: {kickoff.Add(-240 * time.Hour)},
	}}

	got, err := e.FixtureOutlook(context.Background(), 1, "")
	if err != nil {
		t.Fatalf("FixtureOutlook: %v", err)
	}

	byTeamID := make(map[int]TeamOutlook, len(got.TeamsByDifficulty))
	for _, tm := range got.TeamsByDifficulty {
		byTeamID[tm.TeamID] = tm
	}

	home := byTeamID[1]
	if len(home.Fixtures) != 1 || !home.Fixtures[0].Congested {
		t.Errorf("Congested FC's fixture should be flagged congested, got %+v", home.Fixtures)
	}
	away := byTeamID[2]
	if len(away.Fixtures) != 1 || away.Fixtures[0].Congested {
		t.Errorf("Fresh United's fixture should not be flagged congested, got %+v", away.Fixtures)
	}

	// Purely informational: FDR/WeightedFDR must be unaffected by congestion —
	// both teams share the same raw FDR (3) and unset (zero) strength fields,
	// so their FDRs must match regardless of which one is congested.
	if home.Fixtures[0].FDR != away.Fixtures[0].FDR {
		t.Errorf("congested fixture's FDR (%v) differs from the uncongested one (%v); congestion must not affect FDR", home.Fixtures[0].FDR, away.Fixtures[0].FDR)
	}
}

// Without a configured CongestionSource, Congested must stay false (and so
// omitted from JSON via its omitempty tag) — every existing golden fixture
// and NewEngine call site relies on this.
func TestFixtureOutlookOmitsCongestionWhenNotConfigured(t *testing.T) {
	kickoff := time.Date(2026, 1, 10, 15, 0, 0, 0, time.UTC)
	bootstrap, fixtures := congestionOutlookFixture(kickoff)
	e := NewEngine(NewStubClient(bootstrap, fixtures))
	e.Now = func() time.Time { return kickoff }

	got, err := e.FixtureOutlook(context.Background(), 1, "")
	if err != nil {
		t.Fatalf("FixtureOutlook: %v", err)
	}
	for _, tm := range got.TeamsByDifficulty {
		for _, f := range tm.Fixtures {
			if f.Congested {
				t.Errorf("team %d fixture flagged congested with no CongestionSource configured", tm.TeamID)
			}
		}
	}
}
