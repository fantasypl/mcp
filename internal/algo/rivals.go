package algo

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

// Rival intelligence answers "how do I beat the managers around me in this
// mini-league?" It looks RIVAL_WINDOW places above and below the asking
// manager in the standings, compares squads (who has which players the other
// doesn't), flags obvious weaknesses in each rival's XI, and — for the two
// closest rivals only, since it means two extra network fetches each —
// predicts their next transfer from recent history and current squad state.

const rivalWindow = 3

// RivalLeagueError is the shape used both when a league has no standings at
// all and when the user's own squad can't be fetched — two different error
// paths that intentionally produce the same {league_id, error} shape.
type RivalLeagueError struct {
	LeagueID int    `json:"league_id"`
	Error    string `json:"error"`
}

// RivalTeamNotFoundError is distinct from RivalLeagueError only in that the
// league was found (so its name is known) but the requested team isn't a
// member of it.
type RivalTeamNotFoundError struct {
	LeagueID   int    `json:"league_id"`
	LeagueName string `json:"league_name"`
	Error      string `json:"error"`
}

type RivalAnalysisResult struct {
	LeagueID        int              `json:"league_id"`
	LeagueName      string           `json:"league_name"`
	TotalManagers   int              `json:"total_managers"`
	Gameweek        int              `json:"gameweek"`
	StandingsAsOfGW int              `json:"standings_as_of_gw"`
	YourPosition    YourPosition     `json:"your_position"`
	Rivals          []*RivalAnalysis `json:"rivals"`
	Strategy        []string         `json:"strategy"`
}

type YourPosition struct {
	Rank        int    `json:"rank"`
	TotalPoints int    `json:"total_points"`
	GWPoints    int    `json:"gw_points"`
	TeamName    string `json:"team_name"`
}

type RivalAnalysis struct {
	ManagerName        string             `json:"manager_name"`
	TeamName           string             `json:"team_name"`
	TeamID             int                `json:"team_id"`
	Rank               int                `json:"rank"`
	TotalPoints        int                `json:"total_points"`
	GWPoints           int                `json:"gw_points"`
	PointGap           int                `json:"point_gap"`
	GapDirection       string             `json:"gap_direction"`
	Captain            *CaptainComparison `json:"captain"`
	YourDifferentials  []PlayerDetail     `json:"your_differentials"`
	TheirDifferentials []PlayerDetail     `json:"their_differentials"`
	SharedPlayers      int                `json:"shared_players"`
	Weaknesses         []string           `json:"weaknesses"`

	// RecentTransfers and TransferPrediction are populated only for the two
	// closest rivals by point gap. A *[]T rather than []T: a nil pointer
	// omits the key entirely (the key is never set
	// for a non-closest rival), while a non-nil pointer to an empty slice
	// still marshals as "[]" (matching a closest rival who simply made no
	// recent transfers). Plain omitempty on a slice can't tell those two
	// cases apart — both a nil and an empty slice are "empty" to it.
	RecentTransfers    *[]FormattedTransfer `json:"recent_transfers,omitempty"`
	TransferPrediction *TransferPrediction  `json:"transfer_prediction,omitempty"`
}

type CaptainComparison struct {
	RivalCaptain CaptainRef `json:"rival_captain"`
	YourCaptain  CaptainRef `json:"your_captain"`
	SameCaptain  bool       `json:"same_captain"`
}

type CaptainRef struct {
	PlayerID *int   `json:"player_id"`
	Name     string `json:"name"`
	Team     string `json:"team"`
}

type PlayerDetail struct {
	PlayerID      int     `json:"player_id"`
	Name          string  `json:"name"`
	Team          string  `json:"team"`
	TeamFullName  string  `json:"team_full_name"`
	Form          float64 `json:"form"`
	PointsPerGame float64 `json:"points_per_game"`
	Cost          float64 `json:"cost"`
	NextFixture   string  `json:"next_fixture"`
}

type FormattedTransfer struct {
	Gameweek int            `json:"gameweek"`
	In       TransferPlayer `json:"in"`
	Out      TransferPlayer `json:"out"`
}

type TransferPlayer struct {
	PlayerID *int    `json:"player_id"`
	Name     string  `json:"name"`
	Team     string  `json:"team"`
	Cost     float64 `json:"cost"`
}

type TransferPrediction struct {
	LikelyTransfersOut []TransferOutCandidate `json:"likely_transfers_out"`
	LikelyTransfersIn  []TransferInCandidate  `json:"likely_transfers_in"`
}

type TransferOutCandidate struct {
	PlayerID int     `json:"player_id"`
	Name     string  `json:"name"`
	Team     string  `json:"team"`
	Reason   string  `json:"reason"`
	Urgency  float64 `json:"urgency"`
}

type TransferInCandidate struct {
	PlayerID          int     `json:"player_id"`
	Name              string  `json:"name"`
	Team              string  `json:"team"`
	Form              float64 `json:"form"`
	Cost              float64 `json:"cost"`
	TransfersInThisGW int     `json:"transfers_in_this_gw"`
	Score             float64 `json:"score"`
}

// formatPlayerList renders a set of player IDs (already deduplicated —
// callers pass a differential set) with form/fixture context, sorted by form
// descending. The input is a set difference, so its pre-sort order is
// unspecified; break ties by player ID for deterministic output.
func formatPlayerList(playerIDs map[int]bool, playersByID map[int]*fpl.Player, teams map[int]*fpl.Team, fixtureMap map[int][]TeamFixture) []PlayerDetail {
	ids := make([]int, 0, len(playerIDs))
	for id := range playerIDs {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	out := make([]PlayerDetail, 0, len(ids))
	for _, id := range ids {
		p := playersByID[id]
		if p == nil {
			continue
		}
		fixes := fixtureMap[p.Team]
		var opps []string
		for _, f := range fixes {
			venue := "A"
			if f.IsHome {
				venue = "H"
			}
			opps = append(opps, fmt.Sprintf("%s(%s)", shortName(teams[f.Opponent]), venue))
		}
		nextFixture := "Blank"
		if len(opps) > 0 {
			nextFixture = strings.Join(opps, ", ")
		}

		out = append(out, PlayerDetail{
			PlayerID: p.ID, Name: p.WebName, Team: shortName(teams[p.Team]), TeamFullName: fullName(teams[p.Team]),
			Form: p.Form.Float(), PointsPerGame: p.PointsPerGame.Float(),
			Cost: float64(p.NowCost) / 10, NextFixture: nextFixture,
		})
	}

	slices.SortStableFunc(out, func(a, b PlayerDetail) int {
		switch {
		case a.Form > b.Form:
			return -1
		case a.Form < b.Form:
			return 1
		default:
			return 0
		}
	})
	return out
}

// findWeaknesses flags the obvious problems in a rival's starting XI: injury
// flags, blank fixtures, a run of poor form, and a tough upcoming run —
// falling back to "no obvious weaknesses" rather than an empty list, so the
// caller always has something to display.
func findWeaknesses(picks []fpl.Pick, playersByID map[int]*fpl.Player, fixtureMap map[int][]TeamFixture, teams map[int]*fpl.Team) []string {
	var weaknesses []string

	var injured []string
	for _, pick := range picks {
		if p := playersByID[pick.Element]; p != nil && InjuryStatuses[p.Status] {
			injured = append(injured, p.WebName)
		}
	}
	if len(injured) > 0 {
		weaknesses = append(weaknesses, "Injured/doubtful: "+strings.Join(injured, ", "))
	}

	var blanks []string
	for _, pick := range picks {
		if pick.Position > 11 {
			continue
		}
		p := playersByID[pick.Element]
		if p == nil {
			continue
		}
		if len(fixtureMap[p.Team]) == 0 {
			blanks = append(blanks, p.WebName)
		}
	}
	if len(blanks) > 0 {
		weaknesses = append(weaknesses, "Blank GW (no fixture): "+strings.Join(blanks, ", "))
	}

	var poorForm []string
	for _, pick := range picks {
		if pick.Position > 11 {
			continue
		}
		p := playersByID[pick.Element]
		if p == nil {
			continue
		}
		if form := p.Form.Float(); form < 3.0 {
			poorForm = append(poorForm, fmt.Sprintf("%s (%s)", p.WebName, FloatStr(form)))
		}
	}
	if len(poorForm) >= 3 {
		shown := poorForm
		if len(shown) > 4 {
			shown = shown[:4]
		}
		weaknesses = append(weaknesses, "Poor form starters: "+strings.Join(shown, ", "))
	}

	var tough []string
	for _, pick := range picks {
		if pick.Position > 11 {
			continue
		}
		p := playersByID[pick.Element]
		if p == nil {
			continue
		}
		fixes := fixtureMap[p.Team]
		if len(fixes) == 0 {
			continue
		}
		sum := 0.0
		for _, f := range fixes {
			sum += f.FDR
		}
		if avgFDR := sum / float64(len(fixes)); avgFDR >= 4.0 {
			tough = append(tough, fmt.Sprintf("%s vs %s", p.WebName, shortName(teams[fixes[0].Opponent])))
		}
	}
	if len(tough) > 0 {
		shown := tough
		if len(shown) > 3 {
			shown = shown[:3]
		}
		weaknesses = append(weaknesses, "Tough fixtures: "+strings.Join(shown, ", "))
	}

	if len(weaknesses) == 0 {
		weaknesses = append(weaknesses, "No obvious weaknesses — strong squad")
	}
	return weaknesses
}

func formatTransfers(transfers []fpl.ManagerTransfer, playersByID map[int]*fpl.Player, teams map[int]*fpl.Team, currentGW int) []FormattedTransfer {
	out := make([]FormattedTransfer, 0, 6)
	for _, t := range transfers {
		if t.Event < currentGW-2 {
			continue
		}
		if len(out) >= 6 {
			break
		}
		out = append(out, FormattedTransfer{
			Gameweek: t.Event,
			In:       transferPlayerOf(playersByID[t.ElementIn], teams, t.ElementInCost),
			Out:      transferPlayerOf(playersByID[t.ElementOut], teams, t.ElementOutCost),
		})
	}
	return out
}

func transferPlayerOf(p *fpl.Player, teams map[int]*fpl.Team, cost int) TransferPlayer {
	if p == nil {
		return TransferPlayer{Name: "?", Team: "?", Cost: float64(cost) / 10}
	}
	return TransferPlayer{PlayerID: &p.ID, Name: p.WebName, Team: shortName(teams[p.Team]), Cost: float64(cost) / 10}
}

func transferOutReason(p *fpl.Player, fixtureMap map[int][]TeamFixture, teams map[int]*fpl.Team) string {
	var reasons []string
	if InjuryStatuses[p.Status] {
		reasons = append(reasons, "injured/doubtful")
	}
	if form := p.Form.Float(); form < 3.0 {
		reasons = append(reasons, fmt.Sprintf("poor form (%s)", FloatStr(form)))
	}
	fixes := fixtureMap[p.Team]
	if len(fixes) == 0 {
		reasons = append(reasons, "blank GW")
	} else {
		sum := 0.0
		for _, f := range fixes {
			sum += f.FDR
		}
		if avgFDR := sum / float64(len(fixes)); avgFDR >= 4.0 {
			reasons = append(reasons, fmt.Sprintf("tough fixture (%s)", shortName(teams[fixes[0].Opponent])))
		}
	}
	if len(reasons) == 0 {
		return "underperforming"
	}
	return strings.Join(reasons, ", ")
}

// predictNextMove heuristically guesses a rival's next transfer: the most
// urgent candidate to sell (injury, poor form, a tough run, or a falling
// price all add urgency) and the most attractive available buy (in-form,
// good fixtures, rising in popularity).
func predictNextMove(rivalPicks []fpl.Pick, playersByID map[int]*fpl.Player, teams map[int]*fpl.Team, fixtureMap map[int][]TeamFixture, bootstrap *fpl.Bootstrap) *TransferPrediction {
	rivalIDs := make(map[int]bool, len(rivalPicks))
	for _, p := range rivalPicks {
		rivalIDs[p.Element] = true
	}

	var out []TransferOutCandidate
	for _, pick := range rivalPicks {
		if pick.Position > 11 {
			continue
		}
		p := playersByID[pick.Element]
		if p == nil {
			continue
		}
		urgency := 0.0
		if InjuryStatuses[p.Status] {
			urgency += 10.0
		}
		if form := p.Form.Float(); form < 3.0 {
			urgency += (3.0 - form) * 2.0
		}
		fixes := fixtureMap[p.Team]
		if len(fixes) > 0 {
			sum := 0.0
			for _, f := range fixes {
				sum += f.FDR
			}
			if avgFDR := sum / float64(len(fixes)); avgFDR >= 3.5 {
				urgency += (avgFDR - 3.0) * 2.0
			}
		} else {
			urgency += 3.0
		}
		if p.CostChangeEvent < 0 {
			urgency += 2.0
		}

		if urgency > 3.0 {
			out = append(out, TransferOutCandidate{
				PlayerID: p.ID, Name: p.WebName, Team: shortName(teams[p.Team]),
				Reason: transferOutReason(p, fixtureMap, teams), Urgency: Round(urgency, 1),
			})
		}
	}
	slices.SortStableFunc(out, func(a, b TransferOutCandidate) int {
		switch {
		case a.Urgency > b.Urgency:
			return -1
		case a.Urgency < b.Urgency:
			return 1
		default:
			return 0
		}
	})
	if len(out) > 3 {
		out = out[:3]
	}
	if out == nil {
		out = []TransferOutCandidate{}
	}

	in := topTransfersIn(bootstrap, rivalIDs, fixtureMap, teams)
	if len(in) > 3 {
		in = in[:3]
	}
	if in == nil {
		in = []TransferInCandidate{}
	}

	return &TransferPrediction{LikelyTransfersOut: out, LikelyTransfersIn: in}
}

// topTransfersIn finds likely buy targets: in-form, fit, has a fixture, not
// already in the rival's squad. Iterates bootstrap.Elements in its original
// order rather than a map; the stable sort afterward makes tie order depend
// on that original order too.
func topTransfersIn(bootstrap *fpl.Bootstrap, rivalIDs map[int]bool, fixtureMap map[int][]TeamFixture, teams map[int]*fpl.Team) []TransferInCandidate {
	var candidates []TransferInCandidate
	for i := range bootstrap.Elements {
		p := &bootstrap.Elements[i]
		if rivalIDs[p.ID] {
			continue
		}
		form := p.Form.Float()
		if form < 5.0 || InjuryStatuses[p.Status] {
			continue
		}
		fixes := fixtureMap[p.Team]
		if len(fixes) == 0 {
			continue
		}
		sum := 0.0
		for _, f := range fixes {
			sum += f.FDR
		}
		avgFDR := sum / float64(len(fixes))
		score := form*2.0 + (5-avgFDR)*1.5 + float64(p.TransfersInEvent)/100000

		candidates = append(candidates, TransferInCandidate{
			PlayerID: p.ID, Name: p.WebName, Team: shortName(teams[p.Team]),
			Form: form, Cost: float64(p.NowCost) / 10,
			TransfersInThisGW: p.TransfersInEvent, Score: Round(score, 1),
		})
	}
	slices.SortStableFunc(candidates, func(a, b TransferInCandidate) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		default:
			return 0
		}
	})
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	return candidates
}

// RivalAnalysis computes a rival manager's point gap, gap direction, and
// recent-transfer summary relative to the requesting team.
func (e *Engine) RivalAnalysis(ctx context.Context, leagueID, teamID int) (any, error) {
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	fixtures, err := e.client.Fixtures(ctx)
	if err != nil {
		return nil, err
	}
	standingsData, err := e.client.LeagueStandings(ctx, leagueID)
	if err != nil {
		return nil, err
	}

	currentGW := bootstrap.CurrentGameweek()
	nextGW := bootstrap.NextGameweek()
	playersByID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		playersByID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)

	leagueName := standingsData.League.Name
	standings := standingsData.Standings.Results
	if len(standings) == 0 {
		return &RivalLeagueError{LeagueID: leagueID, Error: "League not found or has no standings."}, nil
	}

	userIdx := -1
	for i, s := range standings {
		if s.Entry == teamID {
			userIdx = i
			break
		}
	}
	if userIdx == -1 {
		return &RivalTeamNotFoundError{
			LeagueID: leagueID, LeagueName: leagueName,
			Error: fmt.Sprintf("Team %d not found in this league. Check your team ID.", teamID),
		}, nil
	}
	userStanding := standings[userIdx]

	start := max(0, userIdx-rivalWindow)
	end := min(len(standings), userIdx+rivalWindow+1)
	var rivalStandings []fpl.LeagueEntry
	for _, s := range standings[start:end] {
		if s.Entry != teamID {
			rivalStandings = append(rivalStandings, s)
		}
	}

	userPicks, err := e.client.TeamPicks(ctx, teamID, currentGW)
	if err != nil {
		return &RivalLeagueError{LeagueID: leagueID, Error: "Failed to fetch your squad."}, nil
	}
	userPlayerIDs := make(map[int]bool, len(userPicks.Picks))
	for _, p := range userPicks.Picks {
		userPlayerIDs[p.Element] = true
	}
	var userCaptainID *int
	for _, p := range userPicks.Picks {
		if p.IsCaptain {
			id := p.Element
			userCaptainID = &id
			break
		}
	}

	fixtureMap := buildFixtureMap(fixtures, nextGW, teams)

	var rivalAnalyses []*RivalAnalysis
	// Picks fetched here are reused below for the transfer-prediction step,
	// rather than fetching each rival's squad twice.
	rivalPicksByTeam := make(map[int]*fpl.TeamPicks, len(rivalStandings))
	for _, rival := range rivalStandings {
		rivalPicks, err := e.client.TeamPicks(ctx, rival.Entry, currentGW)
		if err != nil {
			continue
		}
		rivalPicksByTeam[rival.Entry] = rivalPicks

		rivalPlayerIDs := make(map[int]bool, len(rivalPicks.Picks))
		for _, p := range rivalPicks.Picks {
			rivalPlayerIDs[p.Element] = true
		}
		var rivalCaptainID *int
		for _, p := range rivalPicks.Picks {
			if p.IsCaptain {
				id := p.Element
				rivalCaptainID = &id
				break
			}
		}

		yourDiff := diffSet(userPlayerIDs, rivalPlayerIDs)
		theirDiff := diffSet(rivalPlayerIDs, userPlayerIDs)
		shared := 0
		for id := range userPlayerIDs {
			if rivalPlayerIDs[id] {
				shared++
			}
		}

		var captainInfo *CaptainComparison
		if rivalCaptainID != nil {
			captainInfo = &CaptainComparison{
				RivalCaptain: captainRefOf(rivalCaptainID, playersByID, teams),
				YourCaptain:  captainRefOf(userCaptainID, playersByID, teams),
				SameCaptain:  userCaptainID != nil && *rivalCaptainID == *userCaptainID,
			}
		}

		pointGap := userStanding.Total - rival.Total
		gapDirection := "tied"
		if pointGap > 0 {
			gapDirection = "ahead"
		} else if pointGap < 0 {
			gapDirection = "behind"
		}

		rivalAnalyses = append(rivalAnalyses, &RivalAnalysis{
			ManagerName: rival.PlayerName, TeamName: rival.EntryName, TeamID: rival.Entry,
			Rank: rival.Rank, TotalPoints: rival.Total, GWPoints: rival.EventTotal,
			PointGap: pointGap, GapDirection: gapDirection, Captain: captainInfo,
			YourDifferentials:  formatPlayerList(yourDiff, playersByID, teams, fixtureMap),
			TheirDifferentials: formatPlayerList(theirDiff, playersByID, teams, fixtureMap),
			SharedPlayers:      shared,
			Weaknesses:         findWeaknesses(rivalPicks.Picks, playersByID, fixtureMap, teams),
		})
	}

	// Only the two closest rivals by absolute point gap get the extra
	// transfer-history fetch and prediction — expensive, so reserved for
	// whoever's actually worth watching closely.
	closest := slices.Clone(rivalAnalyses)
	slices.SortStableFunc(closest, func(a, b *RivalAnalysis) int {
		return absInt(a.PointGap) - absInt(b.PointGap)
	})
	if len(closest) > 2 {
		closest = closest[:2]
	}

	for _, rival := range closest {
		transfers, err := e.client.ManagerTransfers(ctx, rival.TeamID)
		if err != nil {
			continue
		}
		formatted := formatTransfers(transfers, playersByID, teams, currentGW)
		rival.RecentTransfers = &formatted

		var picks []fpl.Pick
		if rp, ok := rivalPicksByTeam[rival.TeamID]; ok {
			picks = rp.Picks
		}
		rival.TransferPrediction = predictNextMove(picks, playersByID, teams, fixtureMap, bootstrap)
	}

	strategy := buildRivalStrategy(rivalAnalyses)

	currentFinished := false
	for i := range bootstrap.Events {
		if bootstrap.Events[i].ID == currentGW {
			currentFinished = bootstrap.Events[i].Finished
			break
		}
	}
	planningGW := currentGW
	if currentFinished {
		planningGW = nextGW
	}

	return &RivalAnalysisResult{
		LeagueID: leagueID, LeagueName: leagueName, TotalManagers: len(standings),
		Gameweek: planningGW, StandingsAsOfGW: currentGW,
		YourPosition: YourPosition{
			Rank: userStanding.Rank, TotalPoints: userStanding.Total,
			GWPoints: userStanding.EventTotal, TeamName: userStanding.EntryName,
		},
		Rivals: rivalAnalyses, Strategy: strategy,
	}, nil
}

func diffSet(a, b map[int]bool) map[int]bool {
	out := make(map[int]bool)
	for id := range a {
		if !b[id] {
			out[id] = true
		}
	}
	return out
}

func captainRefOf(id *int, playersByID map[int]*fpl.Player, teams map[int]*fpl.Team) CaptainRef {
	if id == nil {
		return CaptainRef{Name: "?", Team: "?"}
	}
	p := playersByID[*id]
	if p == nil {
		return CaptainRef{Name: "?", Team: "?"}
	}
	return CaptainRef{PlayerID: &p.ID, Name: p.WebName, Team: shortName(teams[p.Team])}
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// buildRivalStrategy turns the numeric comparison into short, actionable
// tips, in the reference's priority order: closest rival ahead, closest
// rival behind, differential hauls to watch for, then exploitable weaknesses.
func buildRivalStrategy(rivals []*RivalAnalysis) []string {
	var tips []string

	var ahead, behind []*RivalAnalysis
	for _, r := range rivals {
		if r.PointGap < 0 {
			ahead = append(ahead, r)
		} else if r.PointGap > 0 {
			behind = append(behind, r)
		}
	}

	if len(ahead) > 0 {
		closestAhead := ahead[0]
		for _, r := range ahead {
			if absInt(r.PointGap) < absInt(closestAhead.PointGap) {
				closestAhead = r
			}
		}
		gap := absInt(closestAhead.PointGap)
		tips = append(tips, fmt.Sprintf(
			"You're %dpts behind %s (rank %d). You share %d players — focus on your %d differentials to close the gap.",
			gap, closestAhead.ManagerName, closestAhead.Rank, closestAhead.SharedPlayers, len(closestAhead.YourDifferentials),
		))

		if closestAhead.Captain != nil && !closestAhead.Captain.SameCaptain {
			tips = append(tips, fmt.Sprintf(
				"%s captained %s. If your captain %s outscores theirs, you gain double the margin.",
				closestAhead.ManagerName, closestAhead.Captain.RivalCaptain.Name, closestAhead.Captain.YourCaptain.Name,
			))
		}
	}

	if len(behind) > 0 {
		closestBehind := behind[0]
		for _, r := range behind {
			if r.PointGap < closestBehind.PointGap {
				closestBehind = r
			}
		}
		tips = append(tips, fmt.Sprintf(
			"You're %dpts ahead of %s. They have %d players you don't — watch for hauls from those differentials.",
			closestBehind.PointGap, closestBehind.ManagerName, len(closestBehind.TheirDifferentials),
		))
	}

	top2 := rivals
	if len(top2) > 2 {
		top2 = top2[:2]
	}
	for _, r := range top2 {
		diffs := r.YourDifferentials
		if len(diffs) > 2 {
			diffs = diffs[:2]
		}
		for _, d := range diffs {
			if d.Form >= 5.0 {
				tips = append(tips, fmt.Sprintf(
					"Your differential %s (form %s) isn't in %s's squad — a haul here widens the gap.",
					d.Name, FloatStr(d.Form), r.ManagerName,
				))
				break
			}
		}
	}

	for _, r := range top2 {
		for _, w := range r.Weaknesses {
			if strings.Contains(w, "Injured") || strings.Contains(w, "Blank") {
				tips = append(tips, fmt.Sprintf(
					"%s's vulnerability: %s. They may need to use a transfer on this.",
					r.ManagerName, w,
				))
				break
			}
		}
	}

	if len(tips) == 0 {
		tips = append(tips, "Your league is tight — every captain pick and transfer counts.")
	}
	return tips
}
