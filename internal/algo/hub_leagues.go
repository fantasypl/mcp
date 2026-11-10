package algo

import (
	"context"
	"fmt"
	"sort"

	"golang.org/x/sync/errgroup"

	"github.com/fantasypl/mcp/internal/fpl"
)

// HubLeague is fpl_manager_hub's report on one saved league: where the
// manager stands, their chance of winning it, and how their squad and
// captain compare with what the league's rivals hold.
type HubLeague struct {
	LeagueID int    `json:"league_id"`
	Name     string `json:"name,omitempty"`
	Label    string `json:"label,omitempty"`
	Managers int    `json:"managers,omitempty"` // left out when the league runs past one standings page

	Rank                    int      `json:"rank,omitempty"`
	TotalPoints             int      `json:"total_points,omitempty"`
	PointsBehindLeader      *int     `json:"points_behind_leader,omitempty"`
	PointsBehindPlaceAbove  *int     `json:"points_behind_place_above,omitempty"`
	PointsAheadOfPlaceBelow *int     `json:"points_ahead_of_place_below,omitempty"`
	WinProbability          *float64 `json:"win_probability,omitempty"`

	RivalsCounted int `json:"rivals_counted"`
	// Captain is the hub's captain_choice with this league's ownership.
	Captain     *LeaguePlayer `json:"captain,omitempty"`
	CaptainNote string        `json:"captain_note,omitempty"`
	// Threats are players at least half this league's rivals own and the
	// manager does not, highest effective ownership first.
	Threats []LeaguePlayer `json:"threats"`
	// Differentials are the manager's starters that few of this league's
	// rivals own, so their points gain ground.
	Differentials []LeaguePlayer `json:"differentials"`
	Note          string         `json:"note,omitempty"`
}

// LeaguePlayer is a player with their ownership among one league's rivals.
type LeaguePlayer struct {
	ID              int              `json:"id"`
	Name            string           `json:"name"`
	Team            string           `json:"team"`
	Position        string           `json:"position"`
	LeagueOwnership *LeagueOwnership `json:"league_ownership"`
}

const hubLeagueLimit = 5

func leaguePlayer(id int, t *ownershipTally, playersByID map[int]*fpl.Player, teams map[int]*fpl.Team) LeaguePlayer {
	lp := LeaguePlayer{ID: id, LeagueOwnership: t.of(id)}
	if p := playersByID[id]; p != nil {
		lp.Name, lp.Team, lp.Position = p.WebName, shortName(teams[p.Team]), Position(p.ElementType)
	}
	return lp
}

// hubLeagues builds one HubLeague per saved league.
func (e *Engine) hubLeagues(ctx context.Context, f *RivalField, bootstrap *fpl.Bootstrap, squad []HubSquadEntry, choice *HubCaptainChoice) []HubLeague {
	if f == nil {
		return nil
	}
	playersByID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		playersByID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)
	inSquad := make(map[int]bool, len(squad))
	for _, s := range squad {
		inSquad[s.ElementID] = true
	}
	currentGW, gwsRemaining := bootstrap.CurrentGameweek(), gameweeksRemaining(bootstrap)

	out := make([]HubLeague, len(f.Leagues))
	g, gctx := errgroup.WithContext(ctx)
	for i, fl := range f.Leagues {
		hl := HubLeague{LeagueID: fl.Ref.ID, Label: fl.Ref.Label, Name: fl.Name, Note: fl.Note,
			RivalsCounted: fl.tally.rivals, Threats: []LeaguePlayer{}, Differentials: []LeaguePlayer{}}
		switch {
		case fl.HasNext && fl.You != nil:
			// Only the first standings page is read, so the size is unknown.
			hl.Note = joinNotes(hl.Note, fmt.Sprintf("This league has more than %d managers and only the first standings page is read, so win_probability is shared among those %d.", len(fl.Standings), len(fl.Standings)))
		case !fl.HasNext && fl.Standings != nil:
			hl.Managers = len(fl.Standings)
		}
		if fl.You != nil {
			hl.Rank, hl.TotalPoints = fl.You.Rank, fl.You.Total
			standingGaps(&hl, fl.Standings, f.TeamID)
		}
		if fl.tally.rivals > 0 {
			for _, id := range fl.mostOwnedOutside(inSquad, hubLeagueLimit) {
				hl.Threats = append(hl.Threats, leaguePlayer(id, fl.tally, playersByID, teams))
			}
			for _, s := range squad {
				if s.Starter && pct(fl.tally.owned[s.ElementID], fl.tally.rivals) <= differentialOwnedPct && len(hl.Differentials) < hubLeagueLimit {
					hl.Differentials = append(hl.Differentials, leaguePlayer(s.ElementID, fl.tally, playersByID, teams))
				}
			}
			if choice != nil {
				c := leaguePlayer(choice.ElementID, fl.tally, playersByID, teams)
				hl.Captain = &c
				hl.CaptainNote = captainLeagueNote(fl.tally, choice.ElementID, inSquad, playersByID, teams)
			}
		}
		out[i] = hl

		// Win probability, from the same simulation as league_analyzer.
		if fl.You == nil || currentGW < 1 {
			continue
		}
		g.Go(func() error {
			modelled := fl.Standings[:min(len(fl.Standings), maxModelledManagers)]
			table := leagueWinTable(fl.Ref.ID, modelled, e.leagueHistories(gctx, modelled), currentGW, gwsRemaining)
			for _, row := range table {
				if row.TeamID == f.TeamID {
					out[i].WinProbability = ptr(row.WinProbability)
				}
			}
			return nil
		})
	}
	_ = g.Wait() // every goroutine returns nil
	return out
}

// standingGaps fills in the points to the leader and to the places either
// side of the caller.
func standingGaps(hl *HubLeague, standings []fpl.LeagueEntry, teamID int) {
	for i, s := range standings {
		if s.Entry != teamID {
			continue
		}
		hl.PointsBehindLeader = ptr(standings[0].Total - s.Total)
		if i > 0 {
			hl.PointsBehindPlaceAbove = ptr(standings[i-1].Total - s.Total)
		}
		if i+1 < len(standings) {
			hl.PointsAheadOfPlaceBelow = ptr(s.Total - standings[i+1].Total)
		}
		return
	}
}

// captainLeagueNote compares the hub's captain with the league's most
// captained player, when they differ.
func captainLeagueNote(t *ownershipTally, captainID int, inSquad map[int]bool, playersByID map[int]*fpl.Player, teams map[int]*fpl.Team) string {
	var top []int
	for id := range t.captained {
		top = append(top, id)
	}
	if len(top) == 0 {
		return ""
	}
	sort.Slice(top, func(i, j int) bool {
		a, b := t.captained[top[i]], t.captained[top[j]]
		if a != b {
			return a > b
		}
		return top[i] < top[j]
	})
	leader := top[0]
	name := func(id int) string {
		if p := playersByID[id]; p != nil {
			return fmt.Sprintf("%s (%s)", p.WebName, shortName(teams[p.Team]))
		}
		return fmt.Sprint(id)
	}
	n := t.captained[leader]
	if leader == captainID {
		return fmt.Sprintf("%d of %d rivals captained %s last gameweek, the same as the suggested captain.", n, t.rivals, name(leader))
	}
	if pct(n, t.rivals) < shieldCaptainedPct {
		return ""
	}
	if inSquad[leader] {
		return fmt.Sprintf("%d of %d rivals captained %s last gameweek. You own him, so captaining %s instead is a bet against him.", n, t.rivals, name(leader), name(captainID))
	}
	return fmt.Sprintf("%d of %d rivals captained %s last gameweek, and he is not in your squad.", n, t.rivals, name(leader))
}

func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	return a + " " + b
}
