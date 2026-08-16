package algo

import (
	"context"
	"testing"

	"github.com/ajitem/fpl-intelligence/internal/golden"
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
// the Python assigns a neutral 3.0 difficulty rather than dropping.
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

// The position filter must be applied, and reported back upper-cased.
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
// matching the Python's `if position and position.upper() in POSITION_NAMES`.
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
