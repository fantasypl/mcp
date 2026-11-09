package algo

import (
	"context"
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Issue #53: compare with chosen rivals, not only the managers ranked next to
// you. The rivals scenario's user is Sam Rival (999002, rank 2); Alex
// (999001) is rank 1 and Jo (999003) rank 3.
func chosenRivalsEngine(t *testing.T) *Engine {
	t.Helper()
	e := newRivalsEngine(t)
	stub := e.client.(*StubClient)
	for id, f := range map[int]string{999001: "history_a.json", 999002: "history_b.json", 999003: "history_c.json"} {
		stub.SetHistory(id, loadJSON[*fpl.TeamHistory](t, testdataPath("league_scenario", f)))
	}
	return e
}

func rivalIDs(res *RivalAnalysisResult) []int {
	ids := make([]int, 0, len(res.Rivals))
	for _, r := range res.Rivals {
		ids = append(ids, r.TeamID)
	}
	return ids
}

func TestRivalTrackerTargetRanks(t *testing.T) {
	e := chosenRivalsEngine(t)
	got, err := e.RivalAnalysisWith(context.Background(), leagueScenarioID, rivalsUserTeamID, RivalSelection{TargetRanks: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*RivalAnalysisResult)
	if ids := rivalIDs(res); len(ids) != 1 || ids[0] != 999001 {
		t.Errorf("rivals = %v, want only the leader 999001", ids)
	}
	if res.RivalSelection != "chosen" {
		t.Errorf("rival_selection = %q, want chosen", res.RivalSelection)
	}
}

func TestRivalTrackerRivalTeamIDs(t *testing.T) {
	e := chosenRivalsEngine(t)
	got, err := e.RivalAnalysisWith(context.Background(), leagueScenarioID, rivalsUserTeamID,
		RivalSelection{TeamIDs: []int{999003}, TargetRanks: []int{2}}) // rank 2 is the caller, so it is skipped
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*RivalAnalysisResult)
	if ids := rivalIDs(res); len(ids) != 1 || ids[0] != 999003 {
		t.Errorf("rivals = %v, want only 999003", ids)
	}
}

func TestRivalTrackerUnknownRival(t *testing.T) {
	e := chosenRivalsEngine(t)
	got, err := e.RivalAnalysisWith(context.Background(), leagueScenarioID, rivalsUserTeamID, RivalSelection{TeamIDs: []int{123}, TargetRanks: []int{40}})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := got.(*RivalLeagueError)
	if !ok || !strings.Contains(r.Error, "123") || !strings.Contains(r.Error, "40") {
		t.Errorf("got %#v, want an error naming team 123 and rank 40", got)
	}
}

// Rivals and the caller carry per-gameweek points and chips played, so trends
// and chip timing can be compared.
func TestRivalTrackerGameweekPointsAndChips(t *testing.T) {
	e := chosenRivalsEngine(t)
	got, err := e.RivalAnalysisWith(context.Background(), leagueScenarioID, rivalsUserTeamID, RivalSelection{TargetRanks: []int{1, 3}})
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*RivalAnalysisResult)
	if len(res.YourPosition.GameweekPoints) == 0 {
		t.Error("your_position has no gameweek_points")
	}
	for _, r := range res.Rivals {
		if len(r.GameweekPoints) == 0 {
			t.Errorf("%s has no gameweek_points", r.ManagerName)
		}
		if r.ChipsUsed == nil {
			t.Errorf("%s has no chips_used", r.ManagerName)
		}
	}
	// Alex (999001) played a wildcard in GW3 in the scenario.
	if res.Rivals[0].TeamID == 999001 && (len(res.Rivals[0].ChipsUsed) == 0 || res.Rivals[0].ChipsUsed[0].Gameweek != 3) {
		t.Errorf("Alex chips_used = %+v, want the GW3 wildcard", res.Rivals[0].ChipsUsed)
	}
}

// Without a selection the neighbours are used, as before.
func TestRivalTrackerDefaultsToNeighbours(t *testing.T) {
	e := chosenRivalsEngine(t)
	got, err := e.RivalAnalysisWith(context.Background(), leagueScenarioID, rivalsUserTeamID, RivalSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if got.(*RivalAnalysisResult).RivalSelection != "neighbours" {
		t.Errorf("rival_selection = %q, want neighbours", got.(*RivalAnalysisResult).RivalSelection)
	}
}

// league_analyzer with a team_id: the gap to every modelled manager, and how
// many points a gameweek the caller needs to catch each one ahead.
func TestLeagueCallerGapsToEveryManager(t *testing.T) {
	e := recordedLeagueEngine(t)
	caller := 910015 // rank 15, 536 points
	got, err := e.AnalyzeLeagueForTeam(context.Background(), 1092839, &caller)
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*LeagueAnalysisResult)
	gaps := res.Caller.Gaps
	if len(gaps) != 15 {
		t.Fatalf("got %d gaps, want one per other manager (15)", len(gaps))
	}
	leader, last := gaps[0], gaps[14]
	if leader.Rank != 1 || leader.Gap != -192 || leader.RequiredPPG != 6.86 {
		t.Errorf("gap to leader = %+v, want rank 1, -192, 6.86 a gameweek", leader)
	}
	if last.Rank != 16 || last.Gap != 62 || last.RequiredPPG != 0 {
		t.Errorf("gap to last = %+v, want rank 16, +62, nothing required", last)
	}
	// Detailed rows carry each manager's gameweek points and chips played.
	a := res.Managers[0].(*ManagerAnalysis)
	if len(a.GameweekPoints) != 10 || a.ChipsUsed == nil {
		t.Errorf("leader detail = %d gameweeks, chips %v; want 10 and a chips list", len(a.GameweekPoints), a.ChipsUsed)
	}
}
