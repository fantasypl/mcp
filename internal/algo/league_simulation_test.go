package algo

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Issue #47: league_analyzer normalised a heuristic score across the top 10
// only, so ranks 11-16 got no probability, the caller (rank 15) could not see
// theirs, and the method was undocumented. In league 1092839 the leader, 82
// points clear after GW9, got 14.8%.
//
// testdata/league_1092839 is that league recorded after GW10 (9 Nov 2026),
// with names and IDs anonymised and every score kept: 16 managers, the leader
// on 728 and second on 639.

func recordedLeague(t *testing.T) (*fpl.LeagueStandings, map[string]*fpl.TeamHistory) {
	t.Helper()
	standings := loadJSON[*fpl.LeagueStandings](t, testdataPath("league_1092839", "standings.json"))
	histories := loadJSON[map[string]*fpl.TeamHistory](t, testdataPath("league_1092839", "histories.json"))
	return standings, histories
}

func recordedSimEntries(t *testing.T) []leagueSimEntry {
	t.Helper()
	standings, histories := recordedLeague(t)
	entries := make([]leagueSimEntry, 0, len(standings.Standings.Results))
	for _, r := range standings.Standings.Results {
		h := histories[strconv.Itoa(r.Entry)]
		entries = append(entries, leagueSimEntry{
			TotalPoints: r.Total,
			NetScores:   scoringWeeks(h),
			Chips:       chipsRemaining(h.Chips, 10),
			HasHistory:  true,
		})
	}
	return entries
}

func TestSimulateLeagueCoversEveryManager(t *testing.T) {
	entries := recordedSimEntries(t)
	got := simulateLeagueWinners(entries, 28, 1092839)

	if len(got) != 16 {
		t.Fatalf("got %d probabilities, want one per manager (16)", len(got))
	}
	sum := 0.0
	for i, p := range got {
		if p < 0 || p > 100 {
			t.Errorf("manager %d probability %.1f out of range", i+1, p)
		}
		sum += p
	}
	if math.Abs(sum-100) > 0.5 {
		t.Errorf("probabilities sum to %.2f, want 100", sum)
	}

	// An 89-point lead with 28 gameweeks left and the best scoring rate in
	// the league makes the leader a clear favourite. The old heuristic gave
	// 14.8% for a smaller lead.
	leader := got[0]
	for i, p := range got[1:] {
		if p >= leader {
			t.Errorf("manager %d (%.1f%%) >= the leader (%.1f%%)", i+2, p, leader)
		}
	}
	if leader < 40 {
		t.Errorf("leader probability %.1f%%, want a clear favourite (> 40%%)", leader)
	}
	if got[15] >= got[1] {
		t.Errorf("last place (%.1f%%) >= second (%.1f%%)", got[15], got[1])
	}
}

func TestSimulateLeagueIsDeterministic(t *testing.T) {
	entries := recordedSimEntries(t)
	a := simulateLeagueWinners(entries, 28, 42)
	b := simulateLeagueWinners(entries, 28, 42)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("run differs at %d: %.2f vs %.2f", i, a[i], b[i])
		}
	}
}

func TestSimulateLeagueEdgeCases(t *testing.T) {
	scores := []int{60, 55, 70, 50}
	t.Run("no gameweeks left: the leader has won", func(t *testing.T) {
		got := simulateLeagueWinners([]leagueSimEntry{
			{TotalPoints: 700, NetScores: scores, HasHistory: true},
			{TotalPoints: 690, NetScores: scores, HasHistory: true},
		}, 0, 1)
		if got[0] != 100 || got[1] != 0 {
			t.Errorf("got %v, want [100 0]", got)
		}
	})
	t.Run("a finished tie is shared", func(t *testing.T) {
		got := simulateLeagueWinners([]leagueSimEntry{
			{TotalPoints: 700, NetScores: scores, HasHistory: true},
			{TotalPoints: 700, NetScores: scores, HasHistory: true},
		}, 0, 1)
		if got[0] != 50 || got[1] != 50 {
			t.Errorf("got %v, want [50 50]", got)
		}
	})
	t.Run("identical managers are close to even", func(t *testing.T) {
		got := simulateLeagueWinners([]leagueSimEntry{
			{TotalPoints: 600, NetScores: scores, HasHistory: true},
			{TotalPoints: 600, NetScores: scores, HasHistory: true},
		}, 20, 7)
		if math.Abs(got[0]-50) > 2 {
			t.Errorf("got %v, want about 50/50", got)
		}
	})
	t.Run("an unused chip is worth something", func(t *testing.T) {
		got := simulateLeagueWinners([]leagueSimEntry{
			{TotalPoints: 600, NetScores: scores, HasHistory: true, Chips: []string{"bboost"}},
			{TotalPoints: 600, NetScores: scores, HasHistory: true},
		}, 20, 7)
		if got[0] <= got[1] {
			t.Errorf("got %v, want the manager with a Bench Boost ahead", got)
		}
	})
	t.Run("no history uses the league's average", func(t *testing.T) {
		got := simulateLeagueWinners([]leagueSimEntry{
			{TotalPoints: 600, NetScores: scores, HasHistory: true},
			{TotalPoints: 600},
		}, 20, 7)
		if got[1] <= 0 {
			t.Errorf("got %v, want a probability for the manager with no history", got)
		}
	})
}

// recordedLeagueEngine serves the recorded league through the stub, after
// GW10 with 28 gameweeks left. Squads aren't recorded, so the detailed rows
// all share the synthetic squad; only the probability model is under test.
func recordedLeagueEngine(t *testing.T) *Engine {
	t.Helper()
	b := loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_midseason.json"))
	for i := range b.Events {
		ev := &b.Events[i]
		ev.Finished = ev.ID <= 10
		ev.IsCurrent, ev.IsNext = ev.ID == 10, ev.ID == 11
	}
	c := NewStubClient(b, loadJSON[[]fpl.Fixture](t, testdataPath("fixtures.json")))
	standings, histories := recordedLeague(t)
	c.SetLeague(1092839, standings)
	picks := loadJSON[*fpl.TeamPicks](t, testdataPath("picks_squad1.json"))
	for _, r := range standings.Standings.Results {
		c.SetHistory(r.Entry, histories[strconv.Itoa(r.Entry)])
		c.SetTeamPicks(r.Entry, 10, picks)
	}
	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }
	return e
}

func TestAnalyzeLeagueScoresEveryManagerAndTheCaller(t *testing.T) {
	e := recordedLeagueEngine(t)
	caller := 910015 // rank 15 of 16, as in the issue
	got, err := e.AnalyzeLeagueForTeam(context.Background(), 1092839, &caller)
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*LeagueAnalysisResult)

	if len(res.WinProbabilities) != 16 {
		t.Fatalf("win_probabilities has %d rows, want all 16 managers", len(res.WinProbabilities))
	}
	if res.ManagersModelled != 16 || res.WinProbabilityMethod == "" {
		t.Errorf("managers_modelled = %d, method %q; want 16 and a stated method", res.ManagersModelled, res.WinProbabilityMethod)
	}
	// The detailed rows still cover the top 10, and carry the same figure.
	if len(res.Managers) != maxAnalyzedManagers {
		t.Errorf("managers has %d detailed rows, want %d", len(res.Managers), maxAnalyzedManagers)
	}
	if a := res.Managers[0].(*ManagerAnalysis); a.WinProbability != res.WinProbabilities[0].WinProbability {
		t.Errorf("leader's detailed probability %.1f != table %.1f", a.WinProbability, res.WinProbabilities[0].WinProbability)
	}

	c := res.Caller
	if c == nil {
		t.Fatal("caller row missing for team_id 910015")
	}
	// 728 - 536 = 192 behind with 28 left: 6.86 more per gameweek.
	if c.Rank != 15 || c.PointsGapToLeader != 192 || c.RequiredPPGOverLeader != 6.86 {
		t.Errorf("caller = %+v, want rank 15, gap 192, required_ppg_over_leader 6.86", c)
	}
	if c.WinProbability != res.WinProbabilities[14].WinProbability {
		t.Errorf("caller probability %.1f != table row %.1f", c.WinProbability, res.WinProbabilities[14].WinProbability)
	}
}

// A Bench Boost, Triple Captain or Free Hit week is not repeatable, so it
// must not lift a manager's projected weekly score. Wildcard weeks are
// ordinary scoring and stay in.
func TestNetGameweekScoresDropsOneOffChipWeeks(t *testing.T) {
	history := &fpl.TeamHistory{
		Current: []fpl.HistoryGameweek{
			{Event: 1, Points: 50}, {Event: 2, Points: 95}, {Event: 3, Points: 60, EventTransfersCost: 4},
			{Event: 4, Points: 88}, {Event: 5, Points: 55}, {Event: 6, Points: 70},
		},
		Chips: []fpl.ChipUsage{{Name: "bboost", Event: 2}, {Name: "3xc", Event: 4}, {Name: "wildcard", Event: 6}},
	}
	got := scoringWeeks(history)
	want := []int{50, 56, 55, 70}
	if len(got) != len(want) {
		t.Fatalf("scoringWeeks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scoringWeeks = %v, want %v", got, want)
		}
	}
}
