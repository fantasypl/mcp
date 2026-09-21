package fpl

import (
	"encoding/json"
	"testing"
)

// The FPL fixtures payload reports cards as stats entries keyed by
// "identifier", split into home ("h") and away ("a") lists of
// {value, element}. Fixture.Stats is untyped, so CardEvents is the one place
// that shape is decoded.
func TestFixtureCardEvents(t *testing.T) {
	raw := `{
		"id": 40, "event": 4, "team_h": 1, "team_a": 2,
		"stats": [
			{"identifier": "goals_scored", "h": [{"value": 1, "element": 9}], "a": []},
			{"identifier": "yellow_cards", "h": [{"value": 1, "element": 7}], "a": [{"value": 1, "element": 300}]},
			{"identifier": "red_cards",    "h": [],                          "a": [{"value": 1, "element": 300}]}
		]
	}`
	var f Fixture
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatal(err)
	}

	got := f.CardEvents()
	want := []CardEvent{
		{Player: 7, Team: 1, Yellow: 1},
		{Player: 300, Team: 2, Yellow: 1, Red: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("CardEvents() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("CardEvents()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFixtureCardEventsNoStats(t *testing.T) {
	// Unplayed fixtures carry an empty stats list.
	if got := (Fixture{}).CardEvents(); len(got) != 0 {
		t.Errorf("CardEvents() on a fixture with no stats = %+v, want none", got)
	}
}
