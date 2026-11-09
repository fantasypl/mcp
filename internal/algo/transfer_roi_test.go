package algo

import (
	"context"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Issue #50: did each transfer pay off, net of any hit? Team 2233768 took
// three -4 hits (GW4, GW5, GW6) and sold all three incoming players at the
// GW9 Wildcard. The expected figures were worked out independently from the
// recorded fixture files.
func transferROIEngine(t *testing.T) *Engine {
	t.Helper()
	e := recordedManagerEngine(t)
	stub := e.client.(*StubClient)
	stub.SetTransfers(recordedManagerID, loadJSON[[]fpl.ManagerTransfer](t, testdataPath("manager_2233768", "transfers.json")))
	return e
}

func findTransfer(t *testing.T, res *TransferHistoryResult, gw int, out, in string) TransferHistoryEntry {
	t.Helper()
	for _, tr := range res.Transfers {
		if tr.Gameweek == gw && tr.PlayerOut.Name == out && tr.PlayerIn.Name == in {
			return tr
		}
	}
	t.Fatalf("no GW%d transfer %s -> %s", gw, out, in)
	return TransferHistoryEntry{}
}

func TestTransferROIPerTransfer(t *testing.T) {
	e := transferROIEngine(t)
	got, err := e.TransferHistory(context.Background(), recordedManagerID)
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*TransferHistoryResult)

	// The GW4 hit: Watkins out, Joao Pedro in, held GW4-8, sold at the
	// Wildcard. 26 points against Watkins' 0, minus the 4-point hit.
	jp := findTransfer(t, res, 4, "Watkins", "João Pedro")
	if jp.PointsSinceIn != 26 || jp.PointsSinceOut != 0 || jp.HitCost != 4 || jp.NetGain != 22 {
		t.Errorf("Watkins -> Joao Pedro = in %d, out %d, hit %d, net %d; want 26, 0, 4, 22",
			jp.PointsSinceIn, jp.PointsSinceOut, jp.HitCost, jp.NetGain)
	}
	if jp.HeldGameweeks != 5 || !jp.SoldByWildcard || jp.SoldInGameweek == nil || *jp.SoldInGameweek != 9 {
		t.Errorf("Joao Pedro held %d, sold_by_wildcard %v, sold_in %v; want 5, true, 9", jp.HeldGameweeks, jp.SoldByWildcard, jp.SoldInGameweek)
	}

	// The GW6 hit, Isak -> Kostoulas, lost 3 points and the hit: -7.
	ko := findTransfer(t, res, 6, "Isak", "Kostoulas")
	if ko.NetGain != -7 || ko.HitCost != 4 || ko.HeldGameweeks != 3 {
		t.Errorf("Isak -> Kostoulas = net %d, hit %d, held %d; want -7, 4, 3", ko.NetGain, ko.HitCost, ko.HeldGameweeks)
	}

	// A free transfer still held: no hit, not sold, held through GW10.
	raya := findTransfer(t, res, 4, "Wilson", "Raya")
	if raya.HitCost != 0 || raya.NetGain != 45 || raya.HeldGameweeks != 7 || raya.SoldInGameweek != nil {
		t.Errorf("Wilson -> Raya = hit %d, net %d, held %d, sold %v; want 0, 45, 7, nil", raya.HitCost, raya.NetGain, raya.HeldGameweeks, raya.SoldInGameweek)
	}
}

func TestTransferROISummary(t *testing.T) {
	e := transferROIEngine(t)
	got, err := e.TransferHistory(context.Background(), recordedManagerID)
	if err != nil {
		t.Fatal(err)
	}
	s := got.(*TransferHistoryResult).ROI
	if s == nil {
		t.Fatal("roi summary missing")
	}
	if s.TotalHitCost != 12 || s.Hits != 3 || s.TotalNetGain != 157 {
		t.Errorf("summary = hits %d costing %d, net gain %d; want 3, 12, 157", s.Hits, s.TotalHitCost, s.TotalNetGain)
	}
	if s.HitsReversedWithin5GW != 3 {
		t.Errorf("hits reversed within 5 gameweeks = %d, want 3", s.HitsReversedWithin5GW)
	}

	// Per gameweek, which doesn't depend on which transfer took the hit.
	want := map[int][2]int{4: {80, 76}, 5: {6, 2}, 6: {-10, -14}}
	for _, g := range s.ByGameweek {
		w, ok := want[g.Gameweek]
		if !ok {
			continue
		}
		if g.PointsGained != w[0] || g.NetGain != w[1] || g.HitCost != 4 {
			t.Errorf("GW%d = gained %d, net %d, hit %d; want %d, %d, 4", g.Gameweek, g.PointsGained, g.NetGain, g.HitCost, w[0], w[1])
		}
		delete(want, g.Gameweek)
	}
	if len(want) > 0 {
		t.Errorf("by_gameweek is missing %v", want)
	}
}

// FPL uses free transfers first, so a hit falls on the last transfers made
// in the gameweek.
func TestHitAttributionGoesToTheLastTransfers(t *testing.T) {
	ts := []fpl.ManagerTransfer{
		{Event: 6, ElementIn: 1, ElementOut: 2, Time: "2026-10-03T14:02:30Z"},
		{Event: 6, ElementIn: 3, ElementOut: 4, Time: "2026-10-03T14:40:27Z"},
		{Event: 7, ElementIn: 5, ElementOut: 6, Time: "2026-10-10T10:00:00Z"},
		{Event: 7, ElementIn: 7, ElementOut: 8, Time: "2026-10-10T10:00:01Z"},
		{Event: 7, ElementIn: 9, ElementOut: 10, Time: "2026-10-10T10:00:02Z"},
	}
	got := attributeHits(ts, map[int]int{6: 4, 7: 8})
	want := []int{0, 4, 0, 4, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attributeHits = %v, want %v", got, want)
		}
	}
}
