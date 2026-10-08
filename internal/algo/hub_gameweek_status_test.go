package algo

import (
	"context"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Issue #19: the hub reports the next deadline and time remaining, from the
// same helper as the fpl://status resource.
func TestManagerHubGameweekStatus(t *testing.T) {
	e := hubEngine(t)
	// GW1's deadline in the fixture is 2026-08-21T17:30:00Z.
	e.Now = func() time.Time { return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC) }

	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	gs := got.GameweekStatus
	if gs.NextDeadline != "2026-08-21T17:30:00Z" {
		t.Errorf("NextDeadline = %q, want 2026-08-21T17:30:00Z", gs.NextDeadline)
	}
	if gs.TimeToDeadline != "2d 5h" {
		t.Errorf("TimeToDeadline = %q, want 2d 5h", gs.TimeToDeadline)
	}
	if gs.NextGameweek != 1 || gs.NextGWState != fpl.GameweekUpcoming {
		t.Errorf("next = GW%d %q, want GW1 upcoming", gs.NextGameweek, gs.NextGWState)
	}

	// The hub and the resource share one helper, so they must agree exactly.
	b := e.client.(*StubClient).bootstrap
	if want := fpl.StatusAt(b, e.Now()); gs != want {
		t.Errorf("hub GameweekStatus =\n%+v\nwant fpl.StatusAt =\n%+v", gs, want)
	}
}
