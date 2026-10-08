package algo

import (
	"fmt"

	"github.com/fantasypl/mcp/internal/fpl"
)

// HubCaptainChoice is the hub's single captain recommendation for the
// manager's own starters, combining captain_score and FPL's ep_next.
type HubCaptainChoice struct {
	ElementID    int     `json:"element_id"`
	Name         string  `json:"name"`
	CaptainScore float64 `json:"captain_score"`
	EPNext       float64 `json:"ep_next"`
	// Reason explains the choice in one line, naming the other signal's
	// favourite when the two disagree.
	Reason string `json:"reason"`
}

const (
	// captainTieBand is how close, as a fraction of the leader's
	// captain_score, another starter must be for ep_next to break the tie.
	captainTieBand = 0.05

	// A starter can win a captaincy tiebreak on ep_next only with real
	// attacking threat: expected goal involvements per 90 of at least
	// captainMinXGI90, over at least captainMinMinutes. ep_next is a
	// short-term points estimate that counts clean sheets and defensive
	// contributions, so a cheap defender can top it without being a
	// captaincy option.
	captainMinXGI90   = 0.25
	captainMinMinutes = 270
)

// captainChoice picks one captain from the starters.
//
// The rule: captain_score decides. When another starter's captain_score is
// within captainTieBand (5%) of the leader's, the higher ep_next breaks the
// tie, but only for a player with attacking threat (see captainMinXGI90). The
// captain_score leader always stays eligible. Returns nil when there are no
// starters.
//
// captain_score leads because it already weighs ep_next alongside xG, xA,
// form, fixtures and set pieces, and is the signal the backtests measure.
func captainChoice(squad []HubSquadEntry, players map[int]*fpl.Player) *HubCaptainChoice {
	var starters []*HubSquadEntry
	for i := range squad {
		if squad[i].Starter {
			starters = append(starters, &squad[i])
		}
	}
	if len(starters) == 0 {
		return nil
	}

	leader := starters[0]
	for _, s := range starters[1:] {
		if s.CaptainScore > leader.CaptainScore {
			leader = s
		}
	}

	pick := leader
	for _, s := range starters {
		if s == leader || s.CaptainScore < leader.CaptainScore*(1-captainTieBand) {
			continue
		}
		if s.EPNext > pick.EPNext && attackingThreat(players[s.ElementID]) {
			pick = s
		}
	}

	// ep_next's own favourite, for explaining a disagreement. The leader
	// keeps it on a tie, since a tie is not a disagreement.
	epLeader := leader
	for _, s := range starters {
		if s.EPNext > epLeader.EPNext {
			epLeader = s
		}
	}

	return &HubCaptainChoice{
		ElementID: pick.ElementID, Name: pick.Name,
		CaptainScore: pick.CaptainScore, EPNext: pick.EPNext,
		Reason: captainChoiceReason(pick, leader, epLeader, players),
	}
}

func captainChoiceReason(pick, leader, epLeader *HubSquadEntry, players map[int]*fpl.Player) string {
	switch {
	case pick != leader:
		return fmt.Sprintf("%s: captain_score is within 5%% of %s's (%.1f vs %.1f), so the higher ep_next breaks the tie (%.1f vs %.1f).",
			pick.Name, leader.Name, pick.CaptainScore, leader.CaptainScore, pick.EPNext, leader.EPNext)
	case epLeader.EPNext == 0:
		return fmt.Sprintf("%s leads captain_score (%.1f). FPL has no ep_next to compare yet.", leader.Name, leader.CaptainScore)
	case epLeader == leader:
		return fmt.Sprintf("%s leads both captain_score (%.1f) and ep_next (%.1f).", leader.Name, leader.CaptainScore, leader.EPNext)
	case !attackingThreat(players[epLeader.ElementID]):
		return fmt.Sprintf("%s leads captain_score (%.1f). %s has the higher ep_next (%.1f vs %.1f) but little attacking threat (%s), and ep_next alone doesn't make a captain.",
			leader.Name, leader.CaptainScore, epLeader.Name, epLeader.EPNext, leader.EPNext, threatSummary(players[epLeader.ElementID]))
	default:
		return fmt.Sprintf("%s: captain_score is more than 5%% clear of %s's (%.1f vs %.1f), which outweighs %s's higher ep_next (%.1f vs %.1f).",
			leader.Name, epLeader.Name, leader.CaptainScore, epLeader.CaptainScore, epLeader.Name, epLeader.EPNext, leader.EPNext)
	}
}

// xgi90 is expected goal involvements per 90 minutes, or 0 with no minutes.
func xgi90(p *fpl.Player) float64 {
	if p == nil || p.Minutes == 0 {
		return 0
	}
	return p.ExpectedGoalInvolvements.Float() / (float64(p.Minutes) / 90)
}

// attackingThreat reports whether a player has enough attacking output to be
// a captaincy option on ep_next. Too few minutes is not enough evidence.
func attackingThreat(p *fpl.Player) bool {
	return p != nil && p.Minutes >= captainMinMinutes && xgi90(p) >= captainMinXGI90
}

func threatSummary(p *fpl.Player) string {
	if p == nil || p.Minutes < captainMinMinutes {
		return fmt.Sprintf("under %d minutes played", captainMinMinutes)
	}
	return fmt.Sprintf("xGI/90 %.2f", xgi90(p))
}
