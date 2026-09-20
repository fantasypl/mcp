package algo

import (
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// fixtureWithCards builds a finished fixture in gameweek gw with the given
// card events, in the same wire shape FPL uses.
func fixtureWithCards(id, gw, home, away int, finished bool, events ...fpl.CardEvent) fpl.Fixture {
	f := fpl.Fixture{ID: id, Event: &gw, TeamH: home, TeamA: away, Finished: finished}
	f.Stats = cardStats(home, events...)
	return f
}

// cardStats renders events as FPL's fixture stats: one entry per card kind,
// each split into home ("h") and away ("a") lists of {value, element}.
func cardStats(home int, events ...fpl.CardEvent) []any {
	sides := func(pick func(fpl.CardEvent) int) map[string]any {
		out := map[string]any{"h": []any{}, "a": []any{}}
		for _, ev := range events {
			if pick(ev) == 0 {
				continue
			}
			side := "a"
			if ev.Team == home {
				side = "h"
			}
			out[side] = append(out[side].([]any), map[string]any{"value": float64(pick(ev)), "element": float64(ev.Player)})
		}
		return out
	}
	yellow := sides(func(e fpl.CardEvent) int { return e.Yellow })
	yellow["identifier"] = "yellow_cards"
	red := sides(func(e fpl.CardEvent) int { return e.Red })
	red["identifier"] = "red_cards"
	return []any{yellow, red}
}

// Reinildo's case from issue #1: second-yellow red in GW4, one-match ban,
// available again from GW6. FPL's news text said "until 10 Oct".
func TestEstimateSuspensionSecondYellow(t *testing.T) {
	const player, sunderland, arsenal, city = 300, 2, 1, 3
	fixtures := []fpl.Fixture{
		fixtureWithCards(1, 4, arsenal, sunderland, true, fpl.CardEvent{Player: player, Team: sunderland, Yellow: 1, Red: 1}),
		fixtureWithCards(2, 5, city, sunderland, false),
		fixtureWithCards(3, 6, sunderland, arsenal, false),
	}

	got := EstimateSuspension(player, sunderland, fixtures)
	if got == nil {
		t.Fatal("EstimateSuspension returned nil for a carded player")
	}
	if got.Matches != 1 {
		t.Errorf("Matches = %d, want 1 for a second-yellow red", got.Matches)
	}
	if got.Basis != BasisRedAfterYellow {
		t.Errorf("Basis = %q, want %q", got.Basis, BasisRedAfterYellow)
	}
	if got.RedCardGameweek != 4 {
		t.Errorf("RedCardGameweek = %d, want 4", got.RedCardGameweek)
	}
	if got.MatchesRemaining != 1 {
		t.Errorf("MatchesRemaining = %d, want 1 (GW5 not yet played)", got.MatchesRemaining)
	}
	if got.AvailableFromGW == nil || *got.AvailableFromGW != 6 {
		t.Errorf("AvailableFromGW = %v, want 6", deref(got.AvailableFromGW))
	}
	if got.Confidence != ConfidenceProvisional {
		t.Errorf("Confidence = %q, want provisional: FPL data cannot prove the card type", got.Confidence)
	}
}

// A straight red (no yellow in the same match) is typically 3 matches.
func TestEstimateSuspensionStraightRed(t *testing.T) {
	const player, team, opp = 50, 1, 2
	fixtures := []fpl.Fixture{
		fixtureWithCards(1, 3, team, opp, true, fpl.CardEvent{Player: player, Team: team, Red: 1}),
		fixtureWithCards(2, 4, opp, team, false),
		fixtureWithCards(3, 5, team, opp, false),
		fixtureWithCards(4, 6, opp, team, false),
		fixtureWithCards(5, 7, team, opp, false),
	}

	got := EstimateSuspension(player, team, fixtures)
	if got == nil {
		t.Fatal("EstimateSuspension returned nil for a carded player")
	}
	if got.Matches != 3 || got.Basis != BasisStraightRed {
		t.Errorf("got Matches=%d Basis=%q, want 3 / %q", got.Matches, got.Basis, BasisStraightRed)
	}
	if got.AvailableFromGW == nil || *got.AvailableFromGW != 7 {
		t.Errorf("AvailableFromGW = %v, want 7", deref(got.AvailableFromGW))
	}
	if got.Confidence != ConfidenceProvisional {
		t.Errorf("Confidence = %q, want provisional: a straight red may be 1 (DOGSO) to 3+ (violent conduct)", got.Confidence)
	}
}

// Fixtures already played after the card count as served matches.
func TestEstimateSuspensionServedMatchesReduceRemaining(t *testing.T) {
	const player, team, opp = 50, 1, 2
	fixtures := []fpl.Fixture{
		fixtureWithCards(1, 3, team, opp, true, fpl.CardEvent{Player: player, Team: team, Red: 1}),
		fixtureWithCards(2, 4, opp, team, true),
		fixtureWithCards(3, 5, team, opp, true),
		fixtureWithCards(4, 6, opp, team, false),
	}

	got := EstimateSuspension(player, team, fixtures)
	if got == nil {
		t.Fatal("nil estimate")
	}
	if got.MatchesRemaining != 1 {
		t.Errorf("MatchesRemaining = %d, want 1 (2 of 3 already served)", got.MatchesRemaining)
	}
}

func TestEstimateSuspensionNoRedCard(t *testing.T) {
	const player, team, opp = 50, 1, 2
	fixtures := []fpl.Fixture{
		fixtureWithCards(1, 3, team, opp, true, fpl.CardEvent{Player: player, Team: team, Yellow: 1}),
	}
	if got := EstimateSuspension(player, team, fixtures); got != nil {
		t.Errorf("a lone yellow must not produce a ban estimate, got %+v", got)
	}
}
