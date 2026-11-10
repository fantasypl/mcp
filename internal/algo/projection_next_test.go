package algo

import (
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Issue #42: in 2026-27 FPL's ep_next equals form for every available
// player, so ranking on it ranks on a trailing average. The GW10 case study:
// Gross (6.7 points per game, on set pieces, home to an easy side) had a form
// of 3.2, so an ep_next ranking benched him behind a player on a hot streak.
// The next-gameweek projection must not read form or ep_next at all.

// grossLike is a regular starter with strong season output and a cold
// recent run: form and ep_next 3.2, as in the issue's table.
func grossLike() *fpl.Player {
	p := newPlayer()
	p.ID, p.WebName = 101, "Gross"
	p.PointsPerGame = numOf(6.7)
	p.Form, p.EPNext = numOf(3.2), numOf(3.2)
	p.Starts = 9
	return p
}

// streakyLike has a weaker season but a hot recent run: form and ep_next 6.5.
func streakyLike() *fpl.Player {
	p := newPlayer()
	p.ID, p.WebName = 102, "Streaky"
	p.PointsPerGame = numOf(3.0)
	p.Form, p.EPNext = numOf(6.5), numOf(6.5)
	p.Starts = 9
	return p
}

func TestProjectNextGameweekIgnoresFormAndEPNext(t *testing.T) {
	home := []projectionFixture{{Gameweek: 10, FDR: 2, IsHome: true}}

	gross := projectNextGameweek(grossLike(), home, 9)
	streaky := projectNextGameweek(streakyLike(), home, 9)
	if gross <= streaky {
		t.Fatalf("Gross %.2f <= Streaky %.2f: the projection still follows form/ep_next", gross, streaky)
	}

	// Changing form and ep_next alone must not move the projection.
	hot := grossLike()
	hot.Form, hot.EPNext = numOf(9.9), numOf(9.9)
	if got := projectNextGameweek(hot, home, 9); got != gross {
		t.Errorf("projection moved with form/ep_next: %.2f, want %.2f", got, gross)
	}
}

func TestProjectNextGameweekComponents(t *testing.T) {
	p := grossLike() // 6.7 points per game, 9 starts
	home := []projectionFixture{{Gameweek: 10, FDR: 2, IsHome: true}}

	// 6.7 x 1.0 starts share x 1.15 (FDR 2) x 1.15 (home) = 8.86.
	if got := projectNextGameweek(p, home, 9); got != 8.86 {
		t.Errorf("full projection = %.2f, want 8.86", got)
	}

	t.Run("starts share scales it", func(t *testing.T) {
		rotated := grossLike()
		rotated.Starts = 3 // 3 of 9 team games
		if got := projectNextGameweek(rotated, home, 9); got != 2.95 {
			t.Errorf("rotated projection = %.2f, want 2.95 (a third of 8.86)", got)
		}
	})
	t.Run("a blank gameweek scores zero", func(t *testing.T) {
		if got := projectNextGameweek(p, nil, 9); got != 0 {
			t.Errorf("blank projection = %.2f, want 0", got)
		}
	})
	t.Run("a double gameweek counts both fixtures", func(t *testing.T) {
		double := append(home, projectionFixture{Gameweek: 10, FDR: 3, IsHome: false})
		// 8.86 + 6.7 x 1.0 x 0.95 = 15.23
		if got := projectNextGameweek(p, double, 9); got != 15.23 {
			t.Errorf("double projection = %.2f, want 15.23", got)
		}
	})
	t.Run("a doubtful player scales by chance of playing", func(t *testing.T) {
		d := grossLike()
		d.Status, d.ChanceOfPlayingNextRound = "d", ptr(50)
		if got := projectNextGameweek(d, home, 9); got != 4.43 {
			t.Errorf("doubtful projection = %.2f, want 4.43", got)
		}
	})
	t.Run("before any team game, starts share is 1", func(t *testing.T) {
		if got := projectNextGameweek(p, home, 0); got != 8.86 {
			t.Errorf("preseason projection = %.2f, want 8.86", got)
		}
	})
}

func TestTeamGamesPlayed(t *testing.T) {
	gw := func(n int) *int { return &n }
	fixtures := []fpl.Fixture{
		{Event: gw(1), TeamH: 1, TeamA: 2, Finished: true},
		{Event: gw(2), TeamH: 3, TeamA: 1, Finished: true},
		{Event: gw(3), TeamH: 1, TeamA: 3, Finished: false}, // not played yet
		{Event: nil, TeamH: 2, TeamA: 1, Finished: false},   // unscheduled
	}
	got := teamGamesPlayed(fixtures)
	if got[1] != 2 || got[2] != 1 || got[3] != 1 {
		t.Errorf("teamGamesPlayed = %v, want 1:2 2:1 3:1", got)
	}
}

// Issue #42: captain reasoning said "FPL predicts 7.2pts" from ep_next, which
// is form in 2026-27. Form already has its own clause, so the reasoning must
// not present ep_next as a forecast.
func TestCaptainReasoningDoesNotPresentEPNextAsForecast(t *testing.T) {
	e := NewEngine(nil)
	p := newPlayer()
	p.Form, p.EPNext = numOf(7.2), numOf(7.2)
	// The reasoning is capitalised like Python's str.capitalize, so compare
	// in lower case.
	got := strings.ToLower(e.buildReasoning(p, []TeamFixture{{FDR: 3, IsHome: true}}, 10))
	for _, banned := range []string{"fpl predicts", "fpl expects"} {
		if strings.Contains(got, banned) {
			t.Errorf("reasoning %q presents ep_next as a forecast (%q)", got, banned)
		}
	}
	if !strings.Contains(got, "exceptional form") {
		t.Errorf("reasoning %q lost the form clause", got)
	}
}
