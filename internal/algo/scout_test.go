package algo

import (
	"context"
	"testing"

	"github.com/fantasypl/mcp/internal/golden"
)

func TestSquadScoutMatchesGolden(t *testing.T) {
	bothFixtures(t, func(t *testing.T, _ *Engine, suffix string) {
		e := newEngineWithSquad(t, suffixToFixture(suffix))
		got, err := e.SquadScout(context.Background(), syntheticTeamID)
		if err != nil {
			t.Fatal(err)
		}
		golden.Assert(t, goldenPath("scout"+suffix), got)
	})
}

func TestSquadScoutUnknownTeam(t *testing.T) {
	e := newEngineWithSquad(t, "preseason")
	got, err := e.SquadScout(context.Background(), 424242)
	if err != nil {
		t.Fatalf("expected a soft error result, got a Go error: %v", err)
	}
	if got.Error == "" {
		t.Error("expected a non-empty error message")
	}
	if got.SquadSize != 0 {
		t.Error("an errored result should carry no squad report")
	}
}

// Every case here is cross-checked against a live run of
// app.algorithms.scout._get_suspension_risk — the branch logic (count AND
// gameweek-window conditions, both must hold to match a threshold) is easy
// to get subtly wrong by hand, and an earlier draft of this test did.
func TestSuspensionRiskThresholds(t *testing.T) {
	cases := []struct {
		name                string
		yellow, red, nextGW int
		wantLevel           string
		wantNextThreshold   *int
		wantCardsUntil      *int
	}{
		{"far from any threshold", 1, 0, 5, "low", ptr(5), ptr(4)},
		{"one card from first ban", 4, 0, 5, "high", ptr(5), ptr(1)},
		{"two cards from first ban", 3, 0, 5, "medium", ptr(5), ptr(2)},
		{"at first threshold, judged against second", 5, 0, 20, "low", ptr(10), ptr(5)},
		{"gw window closed for first, count still low", 3, 0, 25, "low", ptr(10), ptr(7)},
		{"one from second ban", 9, 0, 25, "high", ptr(10), ptr(1)},
		{"gw window closed for second, count still low", 8, 0, 35, "low", ptr(15), ptr(7)},
		{"one from the season-long third ban", 14, 0, 38, "high", ptr(15), ptr(1)},
		{"already at 15 — no further threshold", 15, 0, 10, "low", nil, nil},
		{"beyond 15 — no further threshold", 18, 0, 10, "low", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := suspensionRisk(tc.yellow, tc.red, tc.nextGW)
			if got.RiskLevel != tc.wantLevel {
				t.Errorf("RiskLevel = %q, want %q", got.RiskLevel, tc.wantLevel)
			}
			if !intPtrEqual(got.NextThreshold, tc.wantNextThreshold) {
				t.Errorf("NextThreshold = %v, want %v", deref(got.NextThreshold), deref(tc.wantNextThreshold))
			}
			if !intPtrEqual(got.CardsUntilBan, tc.wantCardsUntil) {
				t.Errorf("CardsUntilBan = %v, want %v", deref(got.CardsUntilBan), deref(tc.wantCardsUntil))
			}
		})
	}
}

func TestSuspensionRiskNoteWording(t *testing.T) {
	// Singular vs plural, and red cards appending as a second sentence.
	one := suspensionRisk(4, 0, 5)
	if one.Note == nil || *one.Note != "1 yellow card away from 1-match ban" {
		t.Errorf("got %v", deref(one.Note))
	}

	multi := suspensionRisk(3, 0, 5)
	if multi.Note == nil || *multi.Note != "2 yellow cards away from 1-match ban" {
		t.Errorf("got %v", deref(multi.Note))
	}

	withRed := suspensionRisk(4, 1, 5)
	want := "1 yellow card away from 1-match ban. 1 red card this season"
	if withRed.Note == nil || *withRed.Note != want {
		t.Errorf("got %v, want %q", deref(withRed.Note), want)
	}

	twoReds := suspensionRisk(4, 2, 5)
	if twoReds.Note == nil || *twoReds.Note != "1 yellow card away from 1-match ban. 2 red cards this season" {
		t.Errorf("got %v", deref(twoReds.Note))
	}
}

func TestOrdinal(t *testing.T) {
	cases := map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th"}
	for n, want := range cases {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", n, got, want)
		}
	}
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}
