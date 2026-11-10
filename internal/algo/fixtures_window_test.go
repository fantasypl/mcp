package algo

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
)

// finishedGW1Engine is the midseason fixture moved to the state the issue
// reported: GW1 is current and finished, GW2 is next and not started.
func finishedGW1Engine(t *testing.T) *Engine {
	t.Helper()
	b := loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_midseason.json"))
	for i := range b.Events {
		ev := &b.Events[i]
		ev.IsCurrent, ev.IsNext, ev.Finished = ev.ID == 1, ev.ID == 2, ev.ID == 1
	}
	f := loadJSON[[]fpl.Fixture](t, testdataPath("fixtures.json"))
	e := NewEngine(NewStubClient(b, f))
	e.Now = func() time.Time { return goldenClock }
	return e
}

// Issue #45: after GW9 finished and before the GW10 deadline, a 3-gameweek
// outlook covered GW9 (already played), GW10 and GW11. The window must start
// at the next gameweek.
func TestFixtureOutlookSkipsFinishedGameweek(t *testing.T) {
	e := finishedGW1Engine(t)
	got, err := e.FixtureOutlook(context.Background(), 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{2, 3, 4}; !slices.Equal(got.TargetGameweeks, want) {
		t.Errorf("target_gameweeks = %v, want %v", got.TargetGameweeks, want)
	}
	if got.StartGameweek != 2 {
		t.Errorf("start_gameweek = %d, want 2", got.StartGameweek)
	}
	for _, team := range got.TeamsByDifficulty {
		for _, f := range team.Fixtures {
			if f.Gameweek == 1 {
				t.Errorf("%s includes a GW1 fixture (%s), which has been played", team.Team, f.Opponent)
			}
		}
		if team.FixtureCount > 3 {
			t.Errorf("%s fixture_count = %d over 3 gameweeks without a double", team.Team, team.FixtureCount)
		}
	}
}

// The hub embeds the same outlook, so it inherits the fix.
func TestManagerHubFixtureOutlookSkipsFinishedGameweek(t *testing.T) {
	e := finishedGW1Engine(t)
	stub := e.client.(*StubClient)
	picks := loadJSON[*fpl.TeamPicks](t, testdataPath("picks_squad1.json"))
	stub.SetTeamPicks(syntheticTeamID, 1, picks)
	stub.SetHistory(syntheticTeamID, &fpl.TeamHistory{Current: []fpl.HistoryGameweek{{Event: 1, Points: 58, Value: 1005, Bank: 3}}})

	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range got.FixtureOutlook.TeamsByDifficulty {
		for _, f := range team.Fixtures {
			if f.Gameweek == 1 {
				t.Fatalf("hub fixture_outlook includes played GW1 fixture for %s", team.Team)
			}
		}
	}
}
