package algo

import (
	"context"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Issue #44: season_summary.total_points was gross, while
// manager_status.total_points is FPL's official, net total. The fixture is
// team 2233768's recorded history after GW10: three -4 hits in GW4, GW5 and
// GW6, gross 548, net 536. (After GW9, the issue's snapshot, it was 486 and
// 474.)
func TestHubSeasonSummaryIsNet(t *testing.T) {
	e := hubEngine(t)
	stub := e.client.(*StubClient)
	stub.SetHistory(syntheticTeamID, loadJSON[*fpl.TeamHistory](t, testdataPath("history_2233768.json")))

	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	s := got.SeasonSummary
	if s.TotalPoints != 536 {
		t.Errorf("total_points = %d, want 536 (net of hits, as FPL reports it)", s.TotalPoints)
	}
	if s.GrossPoints != 548 {
		t.Errorf("gross_points = %d, want 548", s.GrossPoints)
	}
	if s.TransferCostTotal != 12 {
		t.Errorf("transfer_cost_total = %d, want 12", s.TransferCostTotal)
	}
	if s.AvgPointsPerGW != 53.6 {
		t.Errorf("avg_points_per_gw = %.1f, want 53.6 (536 / 10)", s.AvgPointsPerGW)
	}
	if len(s.GameweekBreakdown) != 10 {
		t.Fatalf("gameweek_breakdown has %d rows, want 10", len(s.GameweekBreakdown))
	}
	gw4 := s.GameweekBreakdown[3]
	if gw4.GW != 4 || gw4.Points != 69 || gw4.TransferCost != 4 || gw4.NetPoints != 65 {
		t.Errorf("GW4 = %+v, want gw 4, points 69, transfer_cost 4, net_points 65", gw4)
	}
	// The breakdown must add up to both totals.
	gross, net := 0, 0
	for _, g := range s.GameweekBreakdown {
		gross += g.Points
		net += g.NetPoints
	}
	if gross != s.GrossPoints || net != s.TotalPoints {
		t.Errorf("breakdown sums to gross %d, net %d; want %d and %d", gross, net, s.GrossPoints, s.TotalPoints)
	}
}

// A season with no hits has equal gross and net totals, like teams 5501352
// and 5093944 in the issue.
func TestHubSeasonSummaryWithoutHits(t *testing.T) {
	e := hubEngine(t) // one gameweek, 58 points, no hit
	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	s := got.SeasonSummary
	if s.TotalPoints != 58 || s.GrossPoints != 58 || s.TransferCostTotal != 0 {
		t.Errorf("summary = %+v, want 58 net and gross, 0 hits", s)
	}
}
