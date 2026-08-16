package algo

import (
	"context"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/golden"
)

const leagueScenarioID = 12345

// newLeagueEngine wires the 4-manager scenario `fplctl gengolden
// --which=league` produces into a stub client. Manager 999004 has no
// picks or history stubbed at all, simulating a fetch failure. See
// cmd/fplctl/gengolden_scenarios.go for what each manager is built to
// exercise.
func newLeagueEngine(t *testing.T) *Engine {
	t.Helper()
	dir := func(name string) string { return testdataPath("league_scenario", name) }

	c := &stubClient{
		bootstrap: loadJSON[*fpl.Bootstrap](t, dir("bootstrap.json")),
		fixtures:  loadJSON[[]fpl.Fixture](t, dir("fixtures.json")),
		leagues: map[int]*fpl.LeagueStandings{
			leagueScenarioID: loadJSON[*fpl.LeagueStandings](t, dir("standings.json")),
		},
		picks: map[picksKey]*fpl.TeamPicks{
			{999001, 1}: loadJSON[*fpl.TeamPicks](t, dir("picks_a.json")),
			{999002, 1}: loadJSON[*fpl.TeamPicks](t, dir("picks_b.json")),
			{999003, 1}: loadJSON[*fpl.TeamPicks](t, dir("picks_c.json")),
			// 999004 deliberately absent — simulates a fetch failure.
		},
		history: map[int]*fpl.TeamHistory{
			999001: loadJSON[*fpl.TeamHistory](t, dir("history_a.json")),
			999002: loadJSON[*fpl.TeamHistory](t, dir("history_b.json")),
			999003: loadJSON[*fpl.TeamHistory](t, dir("history_c.json")),
			// 999004 deliberately absent.
		},
	}
	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }
	return e
}

func TestAnalyzeLeagueMatchesGolden(t *testing.T) {
	e := newLeagueEngine(t)
	got, err := e.AnalyzeLeague(context.Background(), leagueScenarioID)
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, testdataPath("league_scenario", "golden.json"), got)
}

// A league with no standings at all must produce the distinct
// LeagueNotFound shape, not a Go error.
func TestAnalyzeLeagueNotFound(t *testing.T) {
	c := &stubClient{
		bootstrap: loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_preseason.json")),
		fixtures:  loadJSON[[]fpl.Fixture](t, testdataPath("fixtures.json")),
		leagues: map[int]*fpl.LeagueStandings{
			999: {League: fpl.LeagueInfo{ID: 999, Name: "Empty League"}},
		},
	}
	e := NewEngine(c)
	got, err := e.AnalyzeLeague(context.Background(), 999)
	if err != nil {
		t.Fatal(err)
	}
	nf, ok := got.(*LeagueNotFound)
	if !ok {
		t.Fatalf("got %T, want *LeagueNotFound", got)
	}
	if nf.Error == "" {
		t.Error("expected a non-empty error message")
	}
}

// A manager whose picks or history can't be fetched is returned, not
// dropped, and does not abort analysis of the other managers.
func TestAnalyzeLeaguePartialFetchFailure(t *testing.T) {
	e := newLeagueEngine(t)
	got, err := e.AnalyzeLeague(context.Background(), leagueScenarioID)
	if err != nil {
		t.Fatal(err)
	}
	result := got.(*LeagueAnalysisResult)
	if len(result.Managers) != 4 {
		t.Fatalf("got %d managers, want 4 (including the failed one)", len(result.Managers))
	}

	var failed *ManagerFetchError
	var succeeded int
	for _, m := range result.Managers {
		switch v := m.(type) {
		case *ManagerFetchError:
			failed = v
		case *ManagerAnalysis:
			succeeded++
		}
	}
	if failed == nil {
		t.Fatal("expected one ManagerFetchError entry")
	}
	if failed.TeamName != "Fetch Failure" || failed.WinProbability != 0.0 {
		t.Errorf("got %+v", failed)
	}
	if succeeded != 3 {
		t.Errorf("got %d successful analyses, want 3", succeeded)
	}
}

// Win probabilities across all managers with a valid analysis should sum to
// roughly 100% — the softmax-like normalisation's whole point.
func TestWinProbabilitiesSumToHundred(t *testing.T) {
	e := newLeagueEngine(t)
	got, err := e.AnalyzeLeague(context.Background(), leagueScenarioID)
	if err != nil {
		t.Fatal(err)
	}
	result := got.(*LeagueAnalysisResult)

	sum := 0.0
	for _, m := range result.Managers {
		if a, ok := m.(*ManagerAnalysis); ok {
			sum += a.WinProbability
		}
	}
	if sum < 99.0 || sum > 101.0 {
		t.Errorf("win probabilities sum to %.1f, want ~100", sum)
	}
}

func TestChipsRemainingHalfSplit(t *testing.T) {
	cases := []struct {
		name      string
		chips     []fpl.ChipUsage
		currentGW int
		want      []string
	}{
		{"none used", nil, 5, []string{"3xc", "bboost", "freehit", "wildcard"}},
		{"wildcard used in first half, still first half", []fpl.ChipUsage{{Name: "wildcard", Event: 3}}, 5, []string{"3xc", "bboost", "freehit"}},
		{"first-half chip irrelevant once in second half", []fpl.ChipUsage{{Name: "wildcard", Event: 3}}, 25, []string{"3xc", "bboost", "freehit", "wildcard"}},
		{"second-half wildcard used, now in second half", []fpl.ChipUsage{{Name: "wildcard", Event: 25}}, 30, []string{"3xc", "bboost", "freehit"}},
		{"all four used this half", []fpl.ChipUsage{{Name: "wildcard", Event: 3}, {Name: "bboost", Event: 5}, {Name: "freehit", Event: 7}, {Name: "3xc", Event: 9}}, 10, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chipsRemaining(tc.chips, tc.currentGW)
			want := tc.want
			if want == nil {
				want = []string{}
			}
			if !stringSlicesEqual(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func TestMomentumWindow(t *testing.T) {
	current := []fpl.HistoryGameweek{
		{Event: 1, Points: 40}, {Event: 2, Points: 50}, {Event: 3, Points: 60},
		{Event: 4, Points: 70}, {Event: 5, Points: 80}, {Event: 6, Points: 90},
	}
	// currentGW=6: window is (1, 6], i.e. events 2-6.
	if got := momentum(current, 6); got != 70 {
		t.Errorf("momentum(gw6) = %v, want 70 (avg of events 2-6)", got)
	}
	// currentGW=3: window is (-2, 3], i.e. events 1-3 (only 3 available).
	if got := momentum(current, 3); got != 50 {
		t.Errorf("momentum(gw3) = %v, want 50 (avg of events 1-3)", got)
	}
	if got := momentum(nil, 6); got != 0 {
		t.Errorf("momentum(empty) = %v, want 0", got)
	}
}

func TestFriendlyChipNames(t *testing.T) {
	cases := map[string]string{
		"3xc": "Triple Captain", "bboost": "Bench Boost", "freehit": "Free Hit", "wildcard": "wildcard",
	}
	for in, want := range cases {
		if got := friendlyChipName(in); got != want {
			t.Errorf("friendlyChipName(%q) = %q, want %q", in, got, want)
		}
	}
}
