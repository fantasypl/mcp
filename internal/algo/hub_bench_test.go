package algo

import (
	"context"
	"testing"
)

// Issue #3: the hub reports the manager's own bench (slots 12-15, their real
// auto-sub priority) and separately suggests an order by projected points.
// This is the issue's squad: McGinn (2.8) should come ahead of Matheus N.
// (2.5), and the goalkeeper stays in slot 12 whatever its projection.
func TestSuggestBenchOrder(t *testing.T) {
	squad := []HubSquadEntry{
		{Slot: 1, Starter: true, ElementID: 1, Name: "Starter", Position: "GKP", EPNext: 4.0},
		{Slot: 12, ElementID: 20, Name: "Tzolakis", Position: "GKP", EPNext: 0.5},
		{Slot: 13, ElementID: 21, Name: "Matheus N.", Position: "DEF", EPNext: 2.5},
		{Slot: 14, ElementID: 22, Name: "McGinn", Position: "MID", EPNext: 2.8},
		{Slot: 15, ElementID: 23, Name: "Solanke", Position: "FWD", EPNext: 2.0},
	}

	got := suggestBenchOrder(squad)

	wantNames := []string{"Tzolakis", "McGinn", "Matheus N.", "Solanke"}
	if len(got) != len(wantNames) {
		t.Fatalf("suggested %d bench players, want %d: %+v", len(got), len(wantNames), got)
	}
	for i, want := range wantNames {
		if got[i].Name != want {
			t.Errorf("suggested slot %d = %s, want %s", 12+i, got[i].Name, want)
		}
		if got[i].Slot != 12+i {
			t.Errorf("%s: Slot = %d, want %d", got[i].Name, got[i].Slot, 12+i)
		}
	}
	// The manager's real slot is kept so the difference is visible.
	if got[1].CurrentSlot != 14 || got[2].CurrentSlot != 13 {
		t.Errorf("CurrentSlot for McGinn/Matheus = %d/%d, want 14/13", got[1].CurrentSlot, got[2].CurrentSlot)
	}
}

// Equal projections keep the manager's own relative order, so the suggestion
// never reshuffles players for no reason.
func TestSuggestBenchOrderTiesKeepManagerOrder(t *testing.T) {
	squad := []HubSquadEntry{
		{Slot: 12, ElementID: 1, Name: "GK", Position: "GKP", EPNext: 1},
		{Slot: 13, ElementID: 2, Name: "First", Position: "DEF", EPNext: 2.0},
		{Slot: 14, ElementID: 3, Name: "Second", Position: "MID", EPNext: 2.0},
		{Slot: 15, ElementID: 4, Name: "Third", Position: "FWD", EPNext: 2.0},
	}
	got := suggestBenchOrder(squad)
	for i, want := range []string{"GK", "First", "Second", "Third"} {
		if got[i].Name != want {
			t.Errorf("position %d = %s, want %s", i, got[i].Name, want)
		}
	}
}

// End to end: the suggestion is present, the squad list itself is untouched,
// and every entry now carries the ep_next the suggestion is based on.
func TestManagerHubSuggestedBenchOrder(t *testing.T) {
	e := hubEngine(t)
	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.SuggestedBenchOrder) != got.NumBench {
		t.Fatalf("suggested bench has %d players, want %d", len(got.SuggestedBenchOrder), got.NumBench)
	}
	if got.SuggestedBenchOrder[0].Position != "GKP" {
		t.Errorf("first bench slot = %s, want the goalkeeper", got.SuggestedBenchOrder[0].Position)
	}
	outfield := got.SuggestedBenchOrder[1:]
	for i := 1; i < len(outfield); i++ {
		if outfield[i].EPNext > outfield[i-1].EPNext {
			t.Errorf("outfield bench not descending by ep_next: %+v", outfield)
		}
	}

	// squad still lists the manager's own picks in slot order.
	for i, s := range got.Squad {
		if s.Slot != i+1 {
			t.Errorf("squad[%d].Slot = %d, want %d: squad must stay in the manager's order", i, s.Slot, i+1)
		}
	}
}
