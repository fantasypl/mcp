package algo

import (
	"context"
	"testing"
)

// Issue #4: every starter flagged in squad_health as a poor-form problem, and
// every injured or suspended player, gets a transfer suggestion in the same
// call — not just the single worst-value player the free-transfer count allows.
func TestManagerHubSuggestsForEveryFlaggedPlayer(t *testing.T) {
	e := hubEngine(t)
	stub := e.client.(*StubClient)

	// Force two starters into poor form so the squad has several flags.
	picks := stub.picks[picksKey{syntheticTeamID, 1}]
	forced := map[int]bool{picks.Picks[1].Element: true, picks.Picks[2].Element: true}
	for i := range stub.bootstrap.Elements {
		if forced[stub.bootstrap.Elements[i].ID] {
			stub.bootstrap.Elements[i].Form = 0.5
		}
	}

	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}

	var flagged []int
	for _, p := range got.SquadHealth.PoorFormStarters {
		flagged = append(flagged, p.ElementID)
	}
	for _, p := range got.SquadHealth.InjuredOrDoubtful {
		flagged = append(flagged, p.ElementID)
	}
	if len(flagged) < 2 {
		t.Fatalf("test setup: only %d players flagged, want at least 2", len(flagged))
	}

	suggested := map[int]bool{}
	for _, s := range got.TransferSuggestions {
		suggested[s.TransferOut.ID] = true
	}
	for _, id := range flagged {
		if !suggested[id] {
			t.Errorf("flagged player %d has no transfer suggestion (suggested: %v)", id, suggested)
		}
	}
}

// A doubtful (status "d") player on the bench is still listed in squad_health,
// but doesn't need a forced suggestion: the list would otherwise fill with
// 75%-fit bench fodder. A doubtful starter, and an injured player anywhere,
// still get one.
func TestPlayersNeedingReplacement(t *testing.T) {
	squad := []HubSquadEntry{
		{ElementID: 1, Starter: true, Status: "a", Form: 6},    // fine
		{ElementID: 2, Starter: true, Status: "a", Form: 1.5},  // poor-form starter
		{ElementID: 3, Starter: true, Status: "d", Form: 6},    // doubtful starter
		{ElementID: 4, Starter: true, Status: "s", Form: 6},    // suspended starter
		{ElementID: 5, Starter: false, Status: "d", Form: 6},   // doubtful bench: skipped
		{ElementID: 6, Starter: false, Status: "i", Form: 6},   // injured bench
		{ElementID: 7, Starter: false, Status: "a", Form: 0.5}, // poor form, but on the bench
	}
	got := playersNeedingReplacement(squad)
	want := []int{2, 3, 4, 6}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
