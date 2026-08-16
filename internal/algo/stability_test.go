package algo

import (
	"fmt"
	"time"

	"context"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Sort stability is a correctness requirement, not a preference: tied scores
// is stable, so tied players must keep their bootstrap order.
//
// The golden files do NOT cover this. Scoring the frozen payload produces ties
// among 211 of 564 players, but none of them land in the top 30, and captain
// only emits the top 5-10 — so an unstable sort passes the golden gate by
// luck. This test reaches the property directly.
//
// It bites elsewhere for real: fixtures.go sorts candidates by form, and in
// preseason every player has form 0.0, so that sort is entirely ties.
func TestTiedScoresPreserveInputOrder(t *testing.T) {
	// The pool must be large enough to matter: Go's pdqsort falls back to
	// insertion sort — which happens to be stable — for inputs under about a
	// dozen elements, so a small case cannot distinguish stable from unstable.
	// 240 players across 240 clubs keeps every score tied while forcing the
	// real partitioning path.
	const n = 240
	bs := &fpl.Bootstrap{}
	var fixtures []fpl.Fixture

	for i := 1; i <= n; i++ {
		p := newPlayer()
		p.ID = 100 + i
		p.Team = i
		p.WebName = fmt.Sprintf("P%d", i)
		bs.Elements = append(bs.Elements, *p)
		bs.Teams = append(bs.Teams, fpl.Team{
			ID:        i,
			Name:      fmt.Sprintf("Club %d", i),
			ShortName: fmt.Sprintf("C%d", i),
		})
	}
	// One fixture per pair of clubs, all in GW1 with identical difficulty, so
	// the fixture multiplier is uniform.
	gw := 1
	for i := 1; i <= n; i += 2 {
		fixtures = append(fixtures, fpl.Fixture{
			ID: i, Event: &gw,
			TeamH: i, TeamA: i + 1,
			TeamHDifficulty: 3, TeamADifficulty: 3,
		})
	}

	e := NewEngine(&stubClient{bootstrap: bs, fixtures: fixtures})
	e.Now = func() time.Time { return goldenClock }

	res, err := e.CaptainPicks(context.Background(), &gw, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Picks) != n {
		t.Fatalf("got %d picks, want %d", len(res.Picks), n)
	}

	// Home players score slightly above away ones, so compare within each
	// venue group: order there must follow bootstrap order.
	var homeIDs, awayIDs []int
	for _, p := range res.Picks {
		if p.Fixture.Venue == "Home" {
			homeIDs = append(homeIDs, p.Player.ID)
		} else {
			awayIDs = append(awayIDs, p.Player.ID)
		}
	}
	assertAscending(t, "home", homeIDs)
	assertAscending(t, "away", awayIDs)
}

func assertAscending(t *testing.T, group string, ids []int) {
	t.Helper()
	if len(ids) < 2 {
		t.Fatalf("%s group has %d entries; test is not exercising ties", group, len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] < ids[i-1] {
			t.Errorf("%s group out of input order: %v (unstable sort)", group, ids)
			return
		}
	}
}
