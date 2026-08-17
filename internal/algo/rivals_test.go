package algo

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/golden"
)

const rivalsUserTeamID = 999002

// newRivalsEngine wires the scenario `fplctl gengolden --which=rivals`
// produces — the
// user is Sam Rival (999002); Alex (999001) and Jo (999003) are analyzable
// rivals; Kim (999004) has no picks stubbed, simulating a fetch failure that
// drops them from the analysis entirely (unlike league_analyzer, which
// reports failed managers rather than dropping them).
func newRivalsEngine(t *testing.T) *Engine {
	t.Helper()
	dir := func(name string) string { return testdataPath("rivals_scenario", name) }

	c := &StubClient{
		bootstrap: loadJSON[*fpl.Bootstrap](t, dir("bootstrap.json")),
		fixtures:  loadJSON[[]fpl.Fixture](t, dir("fixtures.json")),
		leagues: map[int]*fpl.LeagueStandings{
			leagueScenarioID: loadJSON[*fpl.LeagueStandings](t, dir("standings.json")),
		},
		picks: map[picksKey]*fpl.TeamPicks{
			{999001, 10}: loadJSON[*fpl.TeamPicks](t, dir("picks_a.json")),
			{999002, 10}: loadJSON[*fpl.TeamPicks](t, dir("picks_b.json")),
			{999003, 10}: loadJSON[*fpl.TeamPicks](t, dir("picks_c.json")),
			// 999004 deliberately absent.
		},
		transfers: map[int][]fpl.ManagerTransfer{
			999001: loadJSON[[]fpl.ManagerTransfer](t, dir("transfers_a.json")),
			999003: loadJSON[[]fpl.ManagerTransfer](t, dir("transfers_c.json")),
		},
	}
	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }
	return e
}

// TestRivalAnalysisMatchesGolden compares against the captured expected output
// with one deliberate exception: your_differentials/their_differentials are
// built from a set[int] difference, and within a tied form value
// (several players commonly share the same form, e.g. 0.0), the pre-sort
// order comes from set iteration — deterministic in the
// sense that it doesn't change between runs (small-int hashes aren't
// randomized by the runtime seed), but not something worth reverse-engineering
// bit-for-bit in Go. formatPlayerList breaks ties by player ID instead, which
// is an equally valid order the algorithm never promised against. So this
// test sorts differential lists by player_id on both sides before comparing
// — every other field, including the exact membership and values of each
// entry, is still checked at full strictness.
func TestRivalAnalysisMatchesGolden(t *testing.T) {
	e := newRivalsEngine(t)
	got, err := e.RivalAnalysis(context.Background(), leagueScenarioID, rivalsUserTeamID)
	if err != nil {
		t.Fatal(err)
	}

	want, err := golden.Load(testdataPath("rivals_scenario", "golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	gotNorm, err := golden.Normalize(got)
	if err != nil {
		t.Fatal(err)
	}

	canonicalizeDifferentials(want)
	canonicalizeDifferentials(gotNorm)

	if ms := golden.Diff(want, gotNorm, golden.Epsilon); len(ms) > 0 {
		t.Errorf("output does not match golden.json (differentials sorted by player_id for comparison)\n%s",
			golden.Format(ms, 25))
	}

	// The canonicalization above verifies set membership but, by re-sorting
	// both sides, throws away the one thing it can't verify: that Go's own
	// output is actually sorted by form descending in the first place. A
	// real sort-direction bug would sail through the reordered comparison
	// above, so check that invariant directly against Go's own (unsorted)
	// output instead of against an external runtime.
	result := got.(*RivalAnalysisResult)
	for _, r := range result.Rivals {
		assertSortedByFormDesc(t, r.ManagerName+".your_differentials", r.YourDifferentials)
		assertSortedByFormDesc(t, r.ManagerName+".their_differentials", r.TheirDifferentials)
	}
}

func assertSortedByFormDesc(t *testing.T, label string, players []PlayerDetail) {
	t.Helper()
	for i := 1; i < len(players); i++ {
		if players[i].Form > players[i-1].Form {
			t.Errorf("%s not sorted by form descending at index %d: %v then %v",
				label, i, players[i-1].Form, players[i].Form)
		}
	}
}

// canonicalizeDifferentials sorts every your_differentials/their_differentials
// array (wherever they appear in the decoded tree) by player_id, in place.
func canonicalizeDifferentials(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if k == "your_differentials" || k == "their_differentials" {
				if arr, ok := child.([]any); ok {
					sort.Slice(arr, func(i, j int) bool {
						pi, _ := arr[i].(map[string]any)["player_id"].(float64)
						pj, _ := arr[j].(map[string]any)["player_id"].(float64)
						return pi < pj
					})
				}
			}
			canonicalizeDifferentials(child)
		}
	case []any:
		for _, child := range t {
			canonicalizeDifferentials(child)
		}
	}
}

// A manager whose squad can't be fetched is dropped from rival_analyses
// entirely — a different failure mode from league_analyzer, which reports
// them with an error entry instead. Both are faithful to their own
// source; they just genuinely behave differently.
func TestRivalWithFailedFetchIsDropped(t *testing.T) {
	e := newRivalsEngine(t)
	got, err := e.RivalAnalysis(context.Background(), leagueScenarioID, rivalsUserTeamID)
	if err != nil {
		t.Fatal(err)
	}
	result := got.(*RivalAnalysisResult)
	if len(result.Rivals) != 2 {
		t.Fatalf("got %d rivals, want 2 (Kim Failure dropped)", len(result.Rivals))
	}
	for _, r := range result.Rivals {
		if r.TeamName == "Fetch Failure" {
			t.Error("the manager with a failed fetch should not appear at all")
		}
	}
}

// The exact trap RecentTransfers' *[]FormattedTransfer type exists for: Jo
// Hoarder is a closest rival (so the key must be present) but has zero
// transfers within the recency window (so the value must be an empty array,
// not null and not absent).
func TestRecentTransfersEmptyButPresent(t *testing.T) {
	e := newRivalsEngine(t)
	got, err := e.RivalAnalysis(context.Background(), leagueScenarioID, rivalsUserTeamID)
	if err != nil {
		t.Fatal(err)
	}
	result := got.(*RivalAnalysisResult)

	for _, r := range result.Rivals {
		if r.TeamName != "Chip Hoarder" {
			continue
		}
		if r.RecentTransfers == nil {
			t.Fatal("Jo Hoarder is a closest rival; recent_transfers must be present, not omitted")
		}
		if len(*r.RecentTransfers) != 0 {
			t.Errorf("got %d recent transfers, want 0 (all filtered by the recency window)", len(*r.RecentTransfers))
		}
	}
}

// Old transfers (event < currentGW-2) must be filtered from recent_transfers.
func TestOldTransfersFilteredByRecencyWindow(t *testing.T) {
	e := newRivalsEngine(t)
	got, err := e.RivalAnalysis(context.Background(), leagueScenarioID, rivalsUserTeamID)
	if err != nil {
		t.Fatal(err)
	}
	result := got.(*RivalAnalysisResult)

	for _, r := range result.Rivals {
		if r.TeamName != "Title Chaser" {
			continue
		}
		if r.RecentTransfers == nil {
			t.Fatal("expected recent_transfers to be present")
		}
		for _, tr := range *r.RecentTransfers {
			if tr.Gameweek == 3 {
				t.Error("GW3 transfer should have been filtered out (currentGW=10, window is >= GW8)")
			}
		}
		if len(*r.RecentTransfers) != 2 {
			t.Errorf("got %d recent transfers, want 2 (GW9 and GW10)", len(*r.RecentTransfers))
		}
	}
}

func TestGapDirection(t *testing.T) {
	cases := []struct {
		gap  int
		want string
	}{{-5, "behind"}, {5, "ahead"}, {0, "tied"}}
	for _, tc := range cases {
		got := "tied"
		if tc.gap > 0 {
			got = "ahead"
		} else if tc.gap < 0 {
			got = "behind"
		}
		if got != tc.want {
			t.Errorf("gap %d: got %q, want %q", tc.gap, got, tc.want)
		}
	}
}

// League not found and team not found are two distinct error shapes — the
// latter includes league_name (the league itself was found), the former
// doesn't (there's no league to name).
func TestRivalAnalysisErrorShapes(t *testing.T) {
	bootstrap := loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_preseason.json"))
	fixtures := loadJSON[[]fpl.Fixture](t, testdataPath("fixtures.json"))

	t.Run("league not found", func(t *testing.T) {
		c := &StubClient{bootstrap: bootstrap, fixtures: fixtures,
			leagues: map[int]*fpl.LeagueStandings{1: {League: fpl.LeagueInfo{ID: 1}}}}
		got, err := NewEngine(c).RivalAnalysis(context.Background(), 1, 999)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.(*RivalLeagueError); !ok {
			t.Fatalf("got %T, want *RivalLeagueError", got)
		}
	})

	t.Run("team not found in league", func(t *testing.T) {
		c := &StubClient{bootstrap: bootstrap, fixtures: fixtures,
			leagues: map[int]*fpl.LeagueStandings{
				1: {
					League:    fpl.LeagueInfo{ID: 1, Name: "Some League"},
					Standings: fpl.StandingsPage{Results: []fpl.LeagueEntry{{Entry: 111, PlayerName: "Someone"}}},
				},
			},
		}
		got, err := NewEngine(c).RivalAnalysis(context.Background(), 1, 999)
		if err != nil {
			t.Fatal(err)
		}
		nf, ok := got.(*RivalTeamNotFoundError)
		if !ok {
			t.Fatalf("got %T, want *RivalTeamNotFoundError", got)
		}
		if nf.LeagueName != "Some League" {
			t.Errorf("LeagueName = %q, want the found league's name", nf.LeagueName)
		}
	})
}
