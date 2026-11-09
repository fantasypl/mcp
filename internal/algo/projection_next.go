package algo

import "github.com/fantasypl/mcp/internal/fpl"

// The next-gameweek projection replaces ep_next as the forward signal the
// hub ranks on (issue #42).
//
// FPL's ep_next was meant to be FPL's own projection, but in 2026-27 FPL sets
// it equal to form (the average over the last 30 days) for every available
// player, and to 0 for most flagged ones. Checked against the live API after
// GW10: all 402 available players with minutes had ep_next == form. Ranking
// on it ranks on a trailing average, which is the opposite of a forecast for
// a regular starter on a cold run.
//
// This projection deliberately reads neither form nor ep_next:
//
//	points per game (season) x starts share x fixture difficulty x venue x availability
//
// summed over the player's fixtures in the gameweek, so a blank scores 0 and
// a double counts twice. The fixture, venue and availability factors are the
// same ones projectExpectedPoints uses for is_hit_worth_it and the
// optimisers, so the two projections differ only in the base rate: season
// points per game scaled by how often the player starts, instead of a
// form-weighted blend.
//
// It is a simple, explainable baseline, not a fitted model. It does not see
// set-piece duties, opponent attack strength or bonus; captain_score does.

// nextGameweekProjectionBasis is the one-line description shown next to
// projected_points in tool output.
const nextGameweekProjectionBasis = "projected_points is this server's estimate for the next gameweek: season points per game, scaled by the share of team games the player started, FPL fixture difficulty, home or away, and chance of playing, summed over that gameweek's fixtures. It does not use form or ep_next; in 2026-27 FPL sets ep_next equal to form."

// projectNextGameweek estimates a player's points across one gameweek's
// fixtures. teamGames is how many games the player's club has finished this
// season, the denominator for the starts share.
func projectNextGameweek(p *fpl.Player, fixtures []projectionFixture, teamGames int) float64 {
	// Starts share is a minutes-security factor: a player who starts every
	// game keeps his full points per game, a rotation option keeps a share.
	// Before the club has played, there is no evidence either way, so the
	// share is 1. It is capped at 1, since a mid-season transfer can have
	// more starts (for two clubs) than his new club has games.
	startsShare := 1.0
	if teamGames > 0 {
		startsShare = float64(p.Starts) / float64(teamGames)
		if startsShare > 1 {
			startsShare = 1
		}
	}
	baseRate := p.PointsPerGame.Float() * startsShare

	// Availability follows projectExpectedPoints: a flagged player scales by
	// his stated chance of playing, and one with no stated chance is out.
	playingPct := 1.0
	if InjuryStatuses[p.Status] {
		if p.ChanceOfPlayingNextRound != nil {
			playingPct = float64(*p.ChanceOfPlayingNextRound) / 100.0
		} else {
			playingPct = 0.0
		}
	}

	total := 0.0
	for _, f := range fixtures {
		difficulty, ok := fdrMultiplier[f.FDR]
		if !ok {
			difficulty = 1.0
		}
		venue := awayPenalty
		if f.IsHome {
			venue = homeBoost
		}
		total += baseRate * difficulty * venue * playingPct
	}
	return Round(total, 2)
}

// teamGamesPlayed counts each club's finished fixtures this season.
func teamGamesPlayed(fixtures []fpl.Fixture) map[int]int {
	out := make(map[int]int)
	for _, f := range fixtures {
		if !f.Finished {
			continue
		}
		out[f.TeamH]++
		out[f.TeamA]++
	}
	return out
}
