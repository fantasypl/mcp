package algo

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/fantasypl/mcp/internal/fpl"
)

// pick_xi chooses the starting XI, formation, bench order and captain for a
// manager's existing 15 (issue #51).
//
// Every player gets a pick score:
//
//	pick_score = projected_points + set_piece_bonus
//
// projected_points is the next-gameweek projection from issue #42 (season
// points per game x starts share x fixture difficulty x venue x chance of
// playing, summed over the gameweek's fixtures), so a blank scores 0 and a
// double counts twice. set_piece_bonus adds a little for first-choice set
// pieces, per fixture, which projected_points does not see. Form is used only
// to break exact ties: the issue's case study is a form-ranked XI benching a
// steady 6.7-points-per-game player on penalties for players on hot runs.
//
// The XI is the best-scoring valid formation (1 GKP, 3-5 DEF, 2-5 MID, 1-3
// FWD). Within a position the best scores always start, so each formation is
// scored as the sum of its top players, as in optimal_squad's chooseLineup.
// pick_xi ranks every formation so it can also show the two nearest
// alternatives and what they would swap.
//
// The captain follows the hub's captain_choice rule (#23, #42): highest
// captain_score among the starters, with projected_points breaking a tie
// within 5% for a player with attacking threat.

// Set-piece bonus per fixture, in points. Judgement calls, not fitted
// values: a first-choice penalty taker is worth roughly half a point a game
// more than his points per game already shows only when a penalty is
// awarded, so these are kept small enough to split near-equal players, not
// to overturn a real gap in season output.
const (
	setPieceBonusPenalties   = 0.5
	setPieceBonusCorners     = 0.2
	setPieceBonusFreeKicks   = 0.2
	pickXIAlternativesToShow = 2
)

const pickXIMethod = "pick_score = projected_points + set_piece_bonus. projected_points is season points per game, scaled by the share of team games the player started, FPL fixture difficulty, home or away, and chance of playing, summed over the gameweek's fixtures (0 for a blank). set_piece_bonus adds 0.5 per fixture for first-choice penalties, 0.2 for first-choice corners and 0.2 for first-choice direct free kicks. Form only breaks exact ties. The XI is the best-scoring valid formation; the captain is the starter with the highest captain_score, with projected_points breaking a tie within 5% for a player with attacking threat."

// PickXIError is returned when the team's picks can't be fetched.
type PickXIError struct {
	Error string `json:"error"`
}

// PickXIComponents are the inputs behind a player's pick score, so the
// ranking can be checked and challenged.
type PickXIComponents struct {
	PointsPerGame   float64  `json:"points_per_game"`
	StartsShare     float64  `json:"starts_share"` // starts / club games played, capped at 1
	Fixtures        int      `json:"fixtures"`     // 0 is a blank, 2 a double
	FDR             int      `json:"fdr,omitempty"`
	Venue           string   `json:"venue,omitempty"` // "H", "A", or "H+A" for a double
	ChanceOfPlaying *int     `json:"chance_of_playing,omitempty"`
	ProjectedPoints float64  `json:"projected_points"`
	SetPieces       []string `json:"set_pieces,omitempty"`
	SetPieceBonus   float64  `json:"set_piece_bonus"`
	CaptainScore    float64  `json:"captain_score"`
	Form            float64  `json:"form"` // tie-break only
}

// PickXIPlayer is one squad player in the suggested line-up.
type PickXIPlayer struct {
	Slot       int              `json:"slot"` // suggested slot, 1-15
	ID         int              `json:"id"`
	Name       string           `json:"name"`
	Team       string           `json:"team"`
	Position   string           `json:"position"`
	Opponent   string           `json:"opponent"` // e.g. "BRE(H)", or "-" for a blank
	PickScore  float64          `json:"pick_score"`
	InYourXI   bool             `json:"in_your_xi"` // starts in the manager's current picks
	Components PickXIComponents `json:"components"`
}

// PickXICaptain is the suggested captain or vice-captain.
type PickXICaptain struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	CaptainScore float64 `json:"captain_score"`
	Reason       string  `json:"reason"`
}

// FormationOption is a formation other than the chosen one.
type FormationOption struct {
	Formation        string   `json:"formation"`
	XIPickScore      float64  `json:"xi_pick_score"`
	PointsDifference float64  `json:"points_difference"` // against the chosen XI; 0 or negative
	Swaps            []string `json:"swaps"`             // e.g. "Saka in for Gabriel"
}

// FixtureExposure is how many squad players come from one fixture, and how
// many of them start.
type FixtureExposure struct {
	Fixture string   `json:"fixture"` // e.g. "BHA v BRE (H)"
	Players []string `json:"players"`
	InXI    int      `json:"in_xi"`
	Benched int      `json:"benched"`
	Note    string   `json:"note,omitempty"`
}

// XIChanges are the moves from the manager's current XI to the suggestion.
type XIChanges struct {
	In  []string `json:"in"`
	Out []string `json:"out"`
}

// PickXIResult is pick_xi's output.
type PickXIResult struct {
	TeamID                int               `json:"team_id"`
	Gameweek              int               `json:"gameweek"`
	SquadFromGameweek     int               `json:"squad_from_gameweek"` // whose picks were read
	Method                string            `json:"method"`
	Formation             string            `json:"formation"`
	XIPickScore           float64           `json:"xi_pick_score"`
	StartingXI            []PickXIPlayer    `json:"starting_xi"`
	Bench                 []PickXIPlayer    `json:"bench"` // slot 12 is the spare goalkeeper
	Captain               *PickXICaptain    `json:"captain"`
	ViceCaptain           *PickXICaptain    `json:"vice_captain"`
	AlternativeFormations []FormationOption `json:"alternative_formations"`
	FixtureExposure       []FixtureExposure `json:"fixture_exposure"`
	ChangesFromYourXI     XIChanges         `json:"changes_from_your_xi"`
}

// PickXI picks the line-up for a manager's squad in gameweek gw (default:
// the next gameweek).
func (e *Engine) PickXI(ctx context.Context, teamID int, gw *int) (any, error) {
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	fixtures, err := e.client.Fixtures(ctx)
	if err != nil {
		return nil, err
	}
	targetGW := bootstrap.NextGameweek()
	if gw != nil {
		targetGW = *gw
	}

	// Before the deadline the target gameweek's picks don't exist yet, so
	// fall back to the current gameweek's squad.
	squadGW := targetGW
	picks, err := e.client.TeamPicks(ctx, teamID, squadGW)
	if err != nil {
		squadGW = bootstrap.CurrentGameweek()
		picks, err = e.client.TeamPicks(ctx, teamID, squadGW)
		if err != nil {
			return &PickXIError{Error: fmt.Sprintf("Could not fetch picks for team %d. Check the team ID is correct.", teamID)}, nil
		}
	}

	byID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		byID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)
	window := buildProjectionWindow(fixtures, targetGW, 1)
	captainFixtures := buildFixtureMap(fixtures, targetGW, teams)
	gamesPlayed := teamGamesPlayed(fixtures)

	var squad []PickXIPlayer
	currentXI := make(map[int]bool, 11)
	for _, pk := range picks.Picks {
		p := byID[pk.Element]
		if p == nil {
			continue
		}
		if pk.Position <= 11 {
			currentXI[p.ID] = true
		}
		squad = append(squad, e.pickXIPlayer(p, window[p.Team], captainFixtures[p.Team], gamesPlayed[p.Team], teams, pk.Position <= 11))
	}

	ranked := rankFormations(squad)
	if len(ranked) == 0 {
		return &PickXIError{Error: fmt.Sprintf("Team %d's squad cannot fill a valid formation.", teamID)}, nil
	}
	best := ranked[0]

	xi, bench := best.xi, best.bench
	for i := range xi {
		xi[i].Slot = i + 1
	}
	for i := range bench {
		bench[i].Slot = 12 + i
	}

	alternatives := make([]FormationOption, 0, pickXIAlternativesToShow)
	for _, alt := range ranked[1:] {
		if len(alternatives) == pickXIAlternativesToShow {
			break
		}
		alternatives = append(alternatives, FormationOption{
			Formation: alt.formation, XIPickScore: Round(alt.score, 2),
			PointsDifference: Round(alt.score-best.score, 2),
			Swaps:            formationSwaps(best.xi, alt.xi),
		})
	}

	captain, vice := pickXICaptains(xi, byID)

	var changes XIChanges
	for _, p := range xi {
		if !currentXI[p.ID] {
			changes.In = append(changes.In, p.Name)
		}
	}
	for _, p := range bench {
		if currentXI[p.ID] {
			changes.Out = append(changes.Out, p.Name)
		}
	}
	if changes.In == nil {
		changes.In, changes.Out = []string{}, []string{}
	}

	return &PickXIResult{
		TeamID: teamID, Gameweek: targetGW, SquadFromGameweek: squadGW, Method: pickXIMethod,
		Formation: best.formation, XIPickScore: Round(best.score, 2),
		StartingXI: xi, Bench: bench, Captain: captain, ViceCaptain: vice,
		AlternativeFormations: alternatives,
		FixtureExposure:       fixtureExposure(xi, bench),
		ChangesFromYourXI:     changes,
	}, nil
}

// pickXIPlayer scores one squad player.
func (e *Engine) pickXIPlayer(p *fpl.Player, fixtures []projectionFixture, captainFixtures []TeamFixture, teamGames int, teams map[int]*fpl.Team, inXI bool) PickXIPlayer {
	projected := projectNextGameweek(p, fixtures, teamGames)

	startsShare := 1.0
	if teamGames > 0 {
		startsShare = min(1, float64(p.Starts)/float64(teamGames))
	}

	var duties []string
	perFixture := 0.0
	if orderOf(p.PenaltiesOrder) == 1 {
		duties = append(duties, "penalties")
		perFixture += setPieceBonusPenalties
	}
	if orderOf(p.CornersAndIndirectFreekicksOrder) == 1 {
		duties = append(duties, "corners")
		perFixture += setPieceBonusCorners
	}
	if orderOf(p.DirectFreekicksOrder) == 1 {
		duties = append(duties, "direct free kicks")
		perFixture += setPieceBonusFreeKicks
	}
	// No bonus for a player who isn't expected to play: a blank, or ruled
	// out. A doubtful player's bonus scales with his chance, as his
	// projection does.
	bonus := 0.0
	if projected > 0 {
		bonus = perFixture * float64(len(fixtures)) * playingChance(p)
	}

	opponents := make([]string, 0, len(fixtures))
	venues := make([]string, 0, len(fixtures))
	fdr := 0
	for i, f := range fixtures {
		v := "A"
		if f.IsHome {
			v = "H"
		}
		opponents = append(opponents, fmt.Sprintf("%s(%s)", shortName(teams[f.Opponent]), v))
		venues = append(venues, v)
		if i == 0 {
			fdr = f.FDR
		}
	}
	opponent := strings.Join(opponents, ", ")
	if opponent == "" {
		opponent = "-"
	}

	var chance *int
	if InjuryStatuses[p.Status] {
		chance = p.ChanceOfPlayingNextRound
		if chance == nil {
			chance = ptr(0)
		}
	}

	return PickXIPlayer{
		ID: p.ID, Name: p.WebName, Team: shortName(teams[p.Team]), Position: Position(p.ElementType),
		Opponent: opponent, PickScore: Round(projected+bonus, 2), InYourXI: inXI,
		Components: PickXIComponents{
			PointsPerGame: p.PointsPerGame.Float(), StartsShare: Round(startsShare, 2),
			Fixtures: len(fixtures), FDR: fdr, Venue: strings.Join(venues, "+"), ChanceOfPlaying: chance,
			ProjectedPoints: projected, SetPieces: duties, SetPieceBonus: Round(bonus, 2),
			CaptainScore: Round(e.scorePlayer(p, captainFixtures), 1), Form: p.Form.Float(),
		},
	}
}

// playingChance is the availability factor projectNextGameweek applies: 1
// for an available player, the stated chance for a flagged one, 0 for a
// flagged one with no stated chance.
func playingChance(p *fpl.Player) float64 {
	if !InjuryStatuses[p.Status] {
		return 1
	}
	if p.ChanceOfPlayingNextRound == nil {
		return 0
	}
	return float64(*p.ChanceOfPlayingNextRound) / 100
}

// comparePickXI orders players best first: higher pick score, then higher
// form (the only place form counts), then lower ID for a stable result.
func comparePickXI(a, b PickXIPlayer) int {
	switch {
	case a.PickScore > b.PickScore:
		return -1
	case a.PickScore < b.PickScore:
		return 1
	case a.Components.Form > b.Components.Form:
		return -1
	case a.Components.Form < b.Components.Form:
		return 1
	default:
		return a.ID - b.ID
	}
}

// rankedFormation is one valid formation's XI and bench.
type rankedFormation struct {
	formation string
	score     float64
	xi        []PickXIPlayer
	bench     []PickXIPlayer
}

// rankFormations scores every valid formation for a squad, best first. Equal
// scores keep enumeration order (fewest defenders, then fewest midfielders).
func rankFormations(squad []PickXIPlayer) []rankedFormation {
	byPos := map[string][]PickXIPlayer{}
	for _, p := range squad {
		byPos[p.Position] = append(byPos[p.Position], p)
	}
	for _, players := range byPos {
		slices.SortStableFunc(players, comparePickXI)
	}
	gkps, defs, mids, fwds := byPos["GKP"], byPos["DEF"], byPos["MID"], byPos["FWD"]
	if len(gkps) == 0 {
		return nil
	}

	var out []rankedFormation
	for d := minDef; d <= maxDef; d++ {
		for m := minMid; m <= maxMid; m++ {
			f := 10 - d - m
			if f < minFwd || f > maxFwd || d > len(defs) || m > len(mids) || f > len(fwds) {
				continue
			}
			rf := rankedFormation{formation: fmt.Sprintf("%d-%d-%d", d, m, f)}
			rf.xi = append(rf.xi, gkps[0])
			var benchOutfield []PickXIPlayer
			for _, group := range []struct {
				players []PickXIPlayer
				n       int
			}{{defs, d}, {mids, m}, {fwds, f}} {
				rf.xi = append(rf.xi, group.players[:group.n]...)
				benchOutfield = append(benchOutfield, group.players[group.n:]...)
			}
			for _, p := range rf.xi {
				rf.score += p.PickScore
			}
			// The spare goalkeeper opens the bench, as FPL requires; the
			// outfield bench follows by pick score.
			rf.bench = append(rf.bench, gkps[1:]...)
			slices.SortStableFunc(benchOutfield, comparePickXI)
			rf.bench = append(rf.bench, benchOutfield...)
			out = append(out, rf)
		}
	}
	slices.SortStableFunc(out, func(a, b rankedFormation) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		default:
			return 0
		}
	})
	return out
}

// formationSwaps lists how an alternative XI differs from the chosen one, as
// "X in for Y" pairs.
func formationSwaps(chosen, alt []PickXIPlayer) []string {
	inChosen := make(map[int]bool, len(chosen))
	for _, p := range chosen {
		inChosen[p.ID] = true
	}
	inAlt := make(map[int]bool, len(alt))
	for _, p := range alt {
		inAlt[p.ID] = true
	}
	var ins, outs []string
	for _, p := range alt {
		if !inChosen[p.ID] {
			ins = append(ins, p.Name)
		}
	}
	for _, p := range chosen {
		if !inAlt[p.ID] {
			outs = append(outs, p.Name)
		}
	}
	swaps := make([]string, 0, len(ins))
	for i := range ins {
		swaps = append(swaps, fmt.Sprintf("%s in for %s", ins[i], outs[i]))
	}
	return swaps
}

// pickXICaptains applies the hub's captain_choice rule to the XI, and picks
// the vice-captain as the next-highest captain_score.
func pickXICaptains(xi []PickXIPlayer, byID map[int]*fpl.Player) (*PickXICaptain, *PickXICaptain) {
	entries := make([]HubSquadEntry, len(xi))
	for i, p := range xi {
		entries[i] = HubSquadEntry{
			Slot: i + 1, Starter: true, ElementID: p.ID, Name: p.Name,
			CaptainScore: p.Components.CaptainScore, ProjectedPoints: p.Components.ProjectedPoints,
		}
	}
	choice := captainChoice(entries, byID)
	if choice == nil {
		return nil, nil
	}
	captain := &PickXICaptain{ID: choice.ElementID, Name: choice.Name, CaptainScore: choice.CaptainScore, Reason: choice.Reason}

	var vice *PickXICaptain
	for _, en := range entries {
		if en.ElementID == captain.ID {
			continue
		}
		if vice == nil || en.CaptainScore > vice.CaptainScore {
			vice = &PickXICaptain{ID: en.ElementID, Name: en.Name, CaptainScore: en.CaptainScore,
				Reason: fmt.Sprintf("Next-highest captain_score in the XI (%.1f).", en.CaptainScore)}
		}
	}
	return captain, vice
}

// fixtureExposure groups the squad by fixture and reports every fixture with
// two or more squad players. Benching two or more players from one match is
// a bet against that match, and the note says so.
func fixtureExposure(xi, bench []PickXIPlayer) []FixtureExposure {
	type group struct {
		label         string
		players       []string
		inXI, benched int
	}
	groups := map[string]*group{}
	var order []string
	add := func(p PickXIPlayer, starting bool) {
		if p.Opponent == "-" || p.Opponent == "" {
			return // a blank has no match to be exposed to
		}
		key := p.Team + "|" + p.Opponent
		g := groups[key]
		if g == nil {
			// "BRE(H)" becomes "BHA v BRE (H)".
			label := p.Team + " v " + strings.ReplaceAll(p.Opponent, "(", " (")
			g = &group{label: label}
			groups[key] = g
			order = append(order, key)
		}
		g.players = append(g.players, p.Name)
		if starting {
			g.inXI++
		} else {
			g.benched++
		}
	}
	for _, p := range xi {
		add(p, true)
	}
	for _, p := range bench {
		add(p, false)
	}

	out := make([]FixtureExposure, 0, len(order))
	for _, key := range order {
		g := groups[key]
		if len(g.players) < 2 {
			continue
		}
		fe := FixtureExposure{Fixture: g.label, Players: g.players, InXI: g.inXI, Benched: g.benched}
		switch {
		case g.benched >= 2:
			fe.Note = fmt.Sprintf("%d of %d players from this match are benched: a bet against %s's returns in it.", g.benched, len(g.players), strings.SplitN(g.label, " v ", 2)[0])
		case g.inXI >= 3:
			fe.Note = fmt.Sprintf("%d starters from this match: a stack on %s.", g.inXI, strings.SplitN(g.label, " v ", 2)[0])
		}
		out = append(out, fe)
	}
	return out
}
