package algo

import (
	"context"
	"testing"
)

// Issue #4: sell candidates are normally the worst `freeTransfers` players by
// value score. A caller can also name players that must get a suggestion, so
// the hub can cover everyone it has flagged rather than only the worst one.
func TestTransferSuggestionsIncludingCoversNamedPlayers(t *testing.T) {
	e := newEngineWithSquad(t, "midseason")
	ctx := context.Background()

	base, err := e.TransferSuggestions(ctx, syntheticTeamID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	baseRes := base.(*TransferSuggestionsResult)
	if len(baseRes.TransferSuggestions) != 1 {
		t.Fatalf("baseline with 1 free transfer gave %d suggestions, want 1", len(baseRes.TransferSuggestions))
	}
	worst := baseRes.TransferSuggestions[0].TransferOut.ID

	// Name two squad players who are not the worst-value one.
	picks := e.client.(*StubClient).picks[picksKey{syntheticTeamID, 1}]
	var named []int
	for _, pick := range picks.Picks {
		if pick.Element != worst && len(named) < 2 {
			named = append(named, pick.Element)
		}
	}

	got, err := e.TransferSuggestionsIncluding(ctx, syntheticTeamID, 1, 0, named)
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*TransferSuggestionsResult)

	covered := map[int]bool{}
	for _, s := range res.TransferSuggestions {
		covered[s.TransferOut.ID] = true
	}
	for _, id := range named {
		if !covered[id] {
			t.Errorf("named player %d has no suggestion", id)
		}
	}
	if !covered[worst] {
		t.Errorf("the worst-value player %d was dropped; existing behaviour must be kept", worst)
	}
	if res.NumSuggestions != len(res.TransferSuggestions) || res.NumSuggestions != 3 {
		t.Errorf("NumSuggestions = %d over %d entries, want 3 (worst + 2 named)", res.NumSuggestions, len(res.TransferSuggestions))
	}
}

// Naming a player who is already a sell candidate must not duplicate them, and
// naming nobody must match TransferSuggestions exactly.
func TestTransferSuggestionsIncludingNoOpCases(t *testing.T) {
	e := newEngineWithSquad(t, "midseason")
	ctx := context.Background()

	base, _ := e.TransferSuggestions(ctx, syntheticTeamID, 1, 0)
	baseRes := base.(*TransferSuggestionsResult)
	worst := baseRes.TransferSuggestions[0].TransferOut.ID

	for name, ids := range map[string][]int{"nobody": nil, "the worst player": {worst}, "an id outside the squad": {999999}} {
		got, err := e.TransferSuggestionsIncluding(ctx, syntheticTeamID, 1, 0, ids)
		if err != nil {
			t.Fatal(err)
		}
		res := got.(*TransferSuggestionsResult)
		if len(res.TransferSuggestions) != 1 || res.TransferSuggestions[0].TransferOut.ID != worst {
			t.Errorf("%s: got %d suggestions, want just the worst player %d", name, len(res.TransferSuggestions), worst)
		}
	}
}
