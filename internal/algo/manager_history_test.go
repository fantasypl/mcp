package algo

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Issue #49: a per-gameweek history of a manager's decisions. The fixture is
// team 2233768's recorded season to GW10 (9 Nov 2026): its history, its picks
// for every gameweek, and each gameweek's live points trimmed to its squad.
// The expected figures below were worked out independently from the same
// files.
const recordedManagerID = 2233768

func recordedManagerEngine(t *testing.T) *Engine {
	t.Helper()
	dir := func(name string) string { return testdataPath("manager_2233768", name) }
	c := NewStubClient(loadJSON[*fpl.Bootstrap](t, dir("bootstrap.json")), nil)
	c.SetHistory(recordedManagerID, loadJSON[*fpl.TeamHistory](t, testdataPath("history_2233768.json")))
	for gw := 1; gw <= 10; gw++ {
		c.SetTeamPicks(recordedManagerID, gw, loadJSON[*fpl.TeamPicks](t, dir(fmt.Sprintf("picks_%d.json", gw))))
		c.SetLive(gw, loadJSON[*fpl.LiveResponse](t, dir(fmt.Sprintf("live_%d.json", gw))))
	}
	return NewEngine(c)
}

func TestManagerHistoryPerGameweek(t *testing.T) {
	e := recordedManagerEngine(t)
	got, err := e.ManagerHistory(context.Background(), recordedManagerID)
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*ManagerHistoryResult)
	if len(res.Gameweeks) != 10 {
		t.Fatalf("got %d gameweeks, want 10", len(res.Gameweeks))
	}

	gw4 := res.Gameweeks[3]
	if gw4.Gameweek != 4 || gw4.Points != 69 || gw4.TransferCost != 4 || gw4.NetPoints != 65 || gw4.PointsOnBench != 19 {
		t.Errorf("GW4 = %+v, want 69 points, 4 hit, 65 net, 19 on the bench", gw4)
	}
	if gw4.GWAverage == 0 || gw4.GWHighest == 0 || gw4.OverallRank == 0 || gw4.GWRank == 0 {
		t.Errorf("GW4 ranks/averages missing: %+v", gw4)
	}
	// Isak captained for 2; Raya's 14 was the best in the XI: 12 forgone.
	if gw4.Captain == nil || gw4.Captain.Name != "Isak" || gw4.Captain.Points != 2 {
		t.Errorf("GW4 captain = %+v, want Isak on 2", gw4.Captain)
	}
	if gw4.BestCaptain == nil || gw4.BestCaptain.Name != "Raya" || gw4.BestCaptain.Points != 14 || gw4.CaptainPointsForgone != 12 {
		t.Errorf("GW4 best = %+v, forgone %d; want Raya 14, forgone 12", gw4.BestCaptain, gw4.CaptainPointsForgone)
	}

	// GW2 had three automatic substitutions.
	gw2 := res.Gameweeks[1]
	if len(gw2.AutoSubs) != 3 || gw2.AutoSubs[0].In != "Reinildo" || gw2.AutoSubs[0].Out != "Senesi" {
		t.Errorf("GW2 auto_subs = %+v, want Reinildo for Senesi first of 3", gw2.AutoSubs)
	}
	// GW3: Isak was the right captain.
	if res.Gameweeks[2].CaptainPointsForgone != 0 {
		t.Errorf("GW3 forgone = %d, want 0", res.Gameweeks[2].CaptainPointsForgone)
	}
	// GW9 was the Wildcard.
	if res.Gameweeks[8].Chip != "wildcard" {
		t.Errorf("GW9 chip = %q, want wildcard", res.Gameweeks[8].Chip)
	}
	if res.Gameweeks[0].ViceCaptain == "" {
		t.Error("vice_captain missing")
	}
}

func TestManagerHistorySummary(t *testing.T) {
	e := recordedManagerEngine(t)
	got, err := e.ManagerHistory(context.Background(), recordedManagerID)
	if err != nil {
		t.Fatal(err)
	}
	s := got.(*ManagerHistoryResult).Summary
	if s.Hits != 3 || s.HitCost != 12 {
		t.Errorf("hits = %d costing %d, want 3 costing 12", s.Hits, s.HitCost)
	}
	if s.PointsOnBench != 114 || s.BenchPointsPerGW != 11.4 {
		t.Errorf("bench = %d (%.1f a gameweek), want 114 (11.4)", s.PointsOnBench, s.BenchPointsPerGW)
	}
	if s.CaptainPointsForgone != 68 {
		t.Errorf("captain points forgone = %d, want 68", s.CaptainPointsForgone)
	}
	if s.NetPoints != 536 || s.GrossPoints != 548 {
		t.Errorf("net %d gross %d, want 536 and 548", s.NetPoints, s.GrossPoints)
	}
	if !strings.Contains(strings.Join(s.ChipsPlayed, ","), "wildcard (GW9)") {
		t.Errorf("chips_played = %v, want the GW9 wildcard", s.ChipsPlayed)
	}
}

func TestManagerHistoryUnknownTeam(t *testing.T) {
	e := recordedManagerEngine(t)
	got, err := e.ManagerHistory(context.Background(), 4242)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := got.(*ManagerHistoryError); !ok || !strings.Contains(r.Error, "4242") {
		t.Errorf("got %#v, want a ManagerHistoryError naming the team", got)
	}
}

// A captain who doesn't play hands the armband to the vice-captain, so the
// vice's points are the captain return and the forgone points are measured
// from them.
func TestCaptainReturnFallsBackToVice(t *testing.T) {
	picks := &fpl.TeamPicks{Picks: []fpl.Pick{
		{Element: 1, Position: 1, Multiplier: 2, IsCaptain: true},
		{Element: 2, Position: 2, Multiplier: 1, IsViceCaptain: true},
		{Element: 3, Position: 3, Multiplier: 1},
	}}
	live := map[int]fpl.LiveStats{1: {Minutes: 0}, 2: {Minutes: 90, TotalPoints: 6}, 3: {Minutes: 90, TotalPoints: 9}}
	names := map[int]string{1: "Out", 2: "Vice", 3: "Best"}
	r := captainReturn(picks, live, names)
	if r.effective.Name != "Vice" || r.effective.Points != 6 || r.best.Name != "Best" || r.forgone != 3 {
		t.Errorf("captainReturn = %+v, want Vice on 6, Best on 9, 3 forgone", r)
	}
}
