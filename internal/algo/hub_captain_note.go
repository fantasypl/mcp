package algo

import "fmt"

// captainSignalNote explains a disagreement between the two captaincy signals
// the hub reports for each starter: captain_score, and the next-gameweek
// projection projected_points. Returns "" when they agree.
//
// They can legitimately disagree. captain_score weighs xG, xA, form,
// fixture difficulty and set-piece duties, while projected_points is a plain
// points estimate from season points per game, starts share, fixture and
// availability (see projection_next.go). The note names each signal's
// favourite so the caller knows why two recommendations differ instead of
// guessing which to trust.
//
// Until issue #42 this compared against ep_next and called it "FPL's own
// projection". In 2026-27 FPL sets ep_next equal to form, so that wording
// was wrong.
//
// Only starters count: a benched player can't be captain. A tie on
// projected_points is not a disagreement, and neither is a missing
// projection: a captain_score leader with zero projected points (a blank
// gameweek, or ruled out) has no figure to disagree with.
func captainSignalNote(squad []HubSquadEntry) string {
	var byScore, byProjection *HubSquadEntry
	for i := range squad {
		s := &squad[i]
		if !s.Starter {
			continue
		}
		if byScore == nil || s.CaptainScore > byScore.CaptainScore {
			byScore = s
		}
		if byProjection == nil || s.ProjectedPoints > byProjection.ProjectedPoints {
			byProjection = s
		}
	}
	if byScore == nil || byScore.ProjectedPoints == 0 || byScore.ProjectedPoints >= byProjection.ProjectedPoints {
		return ""
	}
	return fmt.Sprintf(
		"captain_score favours %s (%.1f, projected_points %.1f) while projected_points favours %s (%.1f, captain_score %.1f). "+
			"captain_score weighs xG, xA, fixture difficulty, form and set-piece duties; projected_points is season points per game scaled by starts, fixture and availability.",
		byScore.Name, byScore.CaptainScore, byScore.ProjectedPoints, byProjection.Name, byProjection.ProjectedPoints, byProjection.CaptainScore)
}
