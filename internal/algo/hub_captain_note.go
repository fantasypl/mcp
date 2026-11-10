package algo

import "fmt"

// captainSignalNote explains a disagreement between the two captaincy signals
// the hub reports for each starter: the server's captain_score, and FPL's own
// ep_next projection. Returns "" when they agree.
//
// They can legitimately disagree. captain_score weighs fixture difficulty,
// form and set-piece duties on top of expected output, while ep_next is FPL's
// own model. The note names each signal's favourite so the caller knows why
// two recommendations differ instead of guessing which to trust.
//
// Only starters count: a benched player can't be captain. A tie on ep_next is
// not a disagreement, and neither is a missing projection: a captain_score
// leader whose ep_next is zero (a new signing, or every player in preseason)
// has no FPL figure to disagree with.
func captainSignalNote(squad []HubSquadEntry) string {
	var byScore, byEP *HubSquadEntry
	for i := range squad {
		s := &squad[i]
		if !s.Starter {
			continue
		}
		if byScore == nil || s.CaptainScore > byScore.CaptainScore {
			byScore = s
		}
		if byEP == nil || s.EPNext > byEP.EPNext {
			byEP = s
		}
	}
	// A zero ep_next for the captain_score leader means FPL has no projection
	// for them (a new signing, say), not a low one, so there is nothing to
	// disagree with.
	if byScore == nil || byScore.EPNext == 0 || byScore.EPNext >= byEP.EPNext {
		return ""
	}
	return fmt.Sprintf(
		"captain_score favours %s (%.1f, ep_next %.1f) while ep_next favours %s (%.1f, captain_score %.1f). "+
			"captain_score weighs fixture difficulty, form and set-piece duties; ep_next is FPL's own projection.",
		byScore.Name, byScore.CaptainScore, byScore.EPNext, byEP.Name, byEP.EPNext, byEP.CaptainScore)
}
