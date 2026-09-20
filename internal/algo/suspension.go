package algo

import (
	"slices"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Basis values describe how a ban length was inferred from card data.
const (
	// BasisRedAfterYellow: the player was shown a yellow and a red in the same
	// match. Almost always a second-yellow dismissal, which carries a one-match
	// ban.
	BasisRedAfterYellow = "red_after_yellow"
	// BasisStraightRed: a red with no yellow in the same match. The default
	// ban is three matches (violent conduct / serious foul play).
	BasisStraightRed = "straight_red"
)

const (
	secondYellowBan = 1
	straightRedBan  = 3
)

// SuspensionEstimate is a ban length worked out from match card events
// rather than read from FPL's free-text news.
//
// It is always provisional. FPL's fixture data records that a red card was
// shown, not why: a yellow followed by a straight red looks the same as a
// second yellow, and a straight red for denying a goalscoring opportunity
// (one match) looks the same as one for violent conduct (three or more).
// Retrospective FA rulings and appeals are not visible either.
type SuspensionEstimate struct {
	// Matches is the expected total ban length.
	Matches int `json:"matches"`
	// MatchesRemaining is how many of those matches are still to be played.
	MatchesRemaining int `json:"matches_remaining"`
	// AvailableFromGW is the first gameweek the player can play again, or nil
	// when that fixture isn't in the schedule.
	AvailableFromGW *int   `json:"available_from_gw"`
	RedCardGameweek int    `json:"red_card_gameweek"`
	Basis           string `json:"basis"`
	Confidence      string `json:"confidence"`
}

// EstimateSuspension estimates the ban following the player's most recent
// red card, or returns nil when the fixtures record none.
//
// The ban covers the team's next `Matches` fixtures after the red-card match;
// finished fixtures among them count as already served. It assumes the player
// serves the ban in consecutive team fixtures across all competitions the
// fixtures list covers, which for FPL's league-only fixtures is a
// simplification: domestic cup matches also count towards a real ban.
func EstimateSuspension(playerID, teamID int, fixtures []fpl.Fixture) *SuspensionEstimate {
	// The team's assigned fixtures in playing order. Gameweek is the primary
	// key so double gameweeks and rescheduled ties still sort sensibly.
	var teamFixtures []fpl.Fixture
	for _, f := range fixtures {
		if _, assigned := f.EventOf(); assigned && (f.TeamH == teamID || f.TeamA == teamID) {
			teamFixtures = append(teamFixtures, f)
		}
	}
	slices.SortStableFunc(teamFixtures, func(a, b fpl.Fixture) int {
		ga, _ := a.EventOf()
		gb, _ := b.EventOf()
		if ga != gb {
			return ga - gb
		}
		if a.KickoffTime != b.KickoffTime {
			if a.KickoffTime < b.KickoffTime {
				return -1
			}
			return 1
		}
		return a.ID - b.ID
	})

	// Most recent finished fixture in which this player was sent off.
	cardIdx, basis := -1, ""
	for i, f := range teamFixtures {
		if !f.Finished {
			continue
		}
		for _, ev := range f.CardEvents() {
			if ev.Player != playerID || ev.Red == 0 {
				continue
			}
			cardIdx = i
			basis = BasisStraightRed
			if ev.Yellow > 0 {
				basis = BasisRedAfterYellow
			}
		}
	}
	if cardIdx < 0 {
		return nil
	}

	matches := straightRedBan
	if basis == BasisRedAfterYellow {
		matches = secondYellowBan
	}

	after := teamFixtures[cardIdx+1:]
	served := 0
	for i := 0; i < matches && i < len(after); i++ {
		if after[i].Finished {
			served++
		}
	}

	cardGW, _ := teamFixtures[cardIdx].EventOf()
	est := &SuspensionEstimate{
		Matches:          matches,
		MatchesRemaining: matches - served,
		RedCardGameweek:  cardGW,
		Basis:            basis,
		Confidence:       ConfidenceProvisional,
	}
	if matches < len(after) {
		gw, _ := after[matches].EventOf()
		est.AvailableFromGW = &gw
	}
	return est
}
