package algo

import (
	"fmt"
	"strings"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Chosen rivals and per-gameweek comparisons (issue #53).
//
// rival_tracker used to compare the caller only with the managers ranked
// within three places of them. For a catch-up question the leaders matter
// more, so rival_team_ids and target_ranks choose the rivals instead. Both
// rival_tracker and league_analyzer now return each analysed manager's
// gameweek points and chips played, from their season history, so score
// trends and chip timing can be compared.

// maxChosenRivals caps each of rival_team_ids and target_ranks. Every chosen
// rival costs two requests (picks and history), plus transfers for the two
// closest.
const maxChosenRivals = 10

// RivalSelection chooses rival_tracker's rivals. Empty means the managers
// ranked within three places of the caller.
type RivalSelection struct {
	TeamIDs     []int
	TargetRanks []int
}

func (s RivalSelection) chosen() bool { return len(s.TeamIDs) > 0 || len(s.TargetRanks) > 0 }

// selectRivals picks the rivals from the standings page, skipping the caller.
// It returns an error message naming any team ID or rank not on the page.
func selectRivals(standings []fpl.LeagueEntry, callerID int, sel RivalSelection) ([]fpl.LeagueEntry, string) {
	wantTeam := make(map[int]bool, len(sel.TeamIDs))
	for _, id := range sel.TeamIDs {
		wantTeam[id] = true
	}
	wantRank := make(map[int]bool, len(sel.TargetRanks))
	for _, r := range sel.TargetRanks {
		wantRank[r] = true
	}

	var out []fpl.LeagueEntry
	foundTeam := map[int]bool{}
	foundRank := map[int]bool{}
	for _, s := range standings {
		matched := false
		if wantTeam[s.Entry] {
			foundTeam[s.Entry], matched = true, true
		}
		if wantRank[s.Rank] {
			foundRank[s.Rank], matched = true, true
		}
		if matched && s.Entry != callerID {
			out = append(out, s)
		}
	}

	var missing []string
	for _, id := range sel.TeamIDs {
		if !foundTeam[id] {
			missing = append(missing, fmt.Sprintf("team %d", id))
		}
	}
	for _, r := range sel.TargetRanks {
		if !foundRank[r] {
			missing = append(missing, fmt.Sprintf("rank %d", r))
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Sprintf("Not on this league's first standings page: %s.", strings.Join(missing, ", "))
	}
	return out, ""
}

// GameweekScore is one gameweek of a manager's season.
type GameweekScore struct {
	Gameweek  int `json:"gameweek"`
	Points    int `json:"points"`     // before hits
	NetPoints int `json:"net_points"` // after hits
}

// gameweekScores lists a season history's gameweeks.
func gameweekScores(h *fpl.TeamHistory) []GameweekScore {
	out := make([]GameweekScore, 0, len(h.Current))
	for _, g := range h.Current {
		out = append(out, GameweekScore{Gameweek: g.Event, Points: g.Points, NetPoints: g.Points - g.EventTransfersCost})
	}
	return out
}

// chipsPlayed lists the chips a history records, with their gameweeks.
func chipsPlayed(h *fpl.TeamHistory) []fpl.ChipUsed {
	out := make([]fpl.ChipUsed, 0, len(h.Chips))
	for _, c := range h.Chips {
		out = append(out, fpl.ChipUsed{Name: c.Name, Gameweek: c.Event})
	}
	return out
}

// LeagueGap is the caller's gap to one other manager.
type LeagueGap struct {
	Rank        int    `json:"rank"`
	TeamID      int    `json:"team_id"`
	ManagerName string `json:"manager_name"`
	// Gap is the caller's total minus theirs: negative when the caller is
	// behind.
	Gap int `json:"gap"`
	// RequiredPPG is how many more points a gameweek than this manager the
	// caller needs to draw level by GW38; 0 when the caller is level or
	// ahead.
	RequiredPPG float64 `json:"required_ppg"`
}

// leagueGaps is the caller's gap to every other modelled manager, in
// standings order.
func leagueGaps(callerID int, table []LeagueWinChance, gwsRemaining int) []LeagueGap {
	var caller *LeagueWinChance
	for i := range table {
		if table[i].TeamID == callerID {
			caller = &table[i]
		}
	}
	if caller == nil {
		return nil
	}
	gaps := make([]LeagueGap, 0, len(table)-1)
	for _, row := range table {
		if row.TeamID == callerID {
			continue
		}
		gap := caller.TotalPoints - row.TotalPoints
		required := 0.0
		if gap < 0 && gwsRemaining > 0 {
			required = Round(float64(-gap)/float64(gwsRemaining), 2)
		}
		gaps = append(gaps, LeagueGap{Rank: row.Rank, TeamID: row.TeamID, ManagerName: row.ManagerName, Gap: gap, RequiredPPG: required})
	}
	return gaps
}
