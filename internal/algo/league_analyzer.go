package algo

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/fantasypl/mcp/internal/fpl"
)

// League analysis answers "who's going to win this mini-league?" without
// needing the asking manager's own team ID — it works from the public
// standings table.
//
// Win probabilities come from a Monte Carlo over the remaining gameweeks for
// every manager on the first standings page (up to 50); see
// league_simulation.go for the model. The top 10 also get a detailed row:
// live squad quality, chips, momentum, team value and injured starters.
// Those are information only and do not feed the probabilities.
//
// A manager whose squad or history can't be fetched doesn't abort the whole
// analysis. A failed squad fetch gives an error row; a failed history fetch
// means the manager is simulated at the league average.

// maxAnalyzedManagers caps how many managers get a detailed row, which needs
// their squad as well as their history.
const maxAnalyzedManagers = 10

// halfwayGW is when chips reset for the second half of the season.
const halfwayGW = 19

// LeagueNotFound is returned when the league has no standings at all — a
// distinct shape from LeagueAnalysisResult, matching the API's
// {"league_id": ..., "error": ...} versus the full result dict.
type LeagueNotFound struct {
	LeagueID int    `json:"league_id"`
	Error    string `json:"error"`
}

type LeagueAnalysisResult struct {
	LeagueID           int    `json:"league_id"`
	LeagueName         string `json:"league_name"`
	TotalManagers      int    `json:"total_managers"`
	Gameweek           int    `json:"gameweek"`
	GameweeksRemaining int    `json:"gameweeks_remaining"`
	AnalyzedTop        int    `json:"analyzed_top"`
	// Managers holds a mix of *ManagerAnalysis and *ManagerFetchError — the
	// The result shape genuinely differs per manager depending on whether
	// their squad/history fetch succeeded, the same reason
	// TransferSuggestions returns `any` instead of a single struct with
	// optional fields: a shared struct with omitempty would silently drop
	// legitimate zero values (bank=0.0, injured_starters=0) that must appear
	// in a real success entry.
	Managers []any    `json:"managers"`
	Insights []string `json:"insights"`

	// WinProbabilityMethod states the model behind every win_probability.
	WinProbabilityMethod string `json:"win_probability_method"`
	// ManagersModelled is how many managers the simulation covers: every
	// manager on the first standings page.
	ManagersModelled int `json:"managers_modelled"`
	// WinProbabilities has one row per modelled manager, in standings order.
	WinProbabilities []LeagueWinChance `json:"win_probabilities"`
	// StandingsNote is set when the league is bigger than one page, to say
	// the managers below it are not modelled.
	StandingsNote string `json:"standings_note,omitempty"`
	// Caller is set when a team_id is given.
	Caller *LeagueCallerRow `json:"caller,omitempty"`
}

// ManagerFetchError is one manager whose squad or history couldn't be
// fetched — retained with an error rather than dropped, so the caller knows they exist and
// why they're excluded from the probability model.
type ManagerFetchError struct {
	ManagerName      string  `json:"manager_name"`
	TeamName         string  `json:"team_name"`
	TeamID           int     `json:"team_id"`
	Rank             int     `json:"rank"`
	TotalPoints      int     `json:"total_points"`
	PointsFromLeader int     `json:"points_from_leader"`
	WinProbability   float64 `json:"win_probability"`
	Error            string  `json:"error"`
}

type ManagerAnalysis struct {
	ManagerName      string   `json:"manager_name"`
	TeamName         string   `json:"team_name"`
	TeamID           int      `json:"team_id"`
	Rank             int      `json:"rank"`
	TotalPoints      int      `json:"total_points"`
	GWPoints         int      `json:"gw_points"`
	PointsFromLeader int      `json:"points_from_leader"`
	SquadQuality     float64  `json:"squad_quality"`
	ChipsRemaining   []string `json:"chips_remaining"`
	MomentumLast5GW  float64  `json:"momentum_last_5gw"`
	Bank             float64  `json:"bank"`
	TeamValue        float64  `json:"team_value"`
	InjuredStarters  int      `json:"injured_starters"`
	WinProbability   float64  `json:"win_probability"` // from the simulation; see LeagueAnalysisResult.WinProbabilityMethod
}

// calculateSquadQuality scores the starting XI's recent output plus how easy
// their upcoming fixtures are. A blank gameweek is a flat penalty rather than
// an exclusion, so a squad full of blanking players still scores (badly)
// rather than looking artificially average.
func calculateSquadQuality(picks []fpl.Pick, playersByID map[int]*fpl.Player, fixtureMap map[int][]TeamFixture) float64 {
	total := 0.0
	for _, pick := range picks {
		if pick.Position > 11 {
			continue
		}
		p := playersByID[pick.Element]
		if p == nil {
			continue
		}
		form := p.Form.Float()
		ppg := p.PointsPerGame.Float()

		fixtureBonus := -2.0
		if fixes := fixtureMap[p.Team]; len(fixes) > 0 {
			sum := 0.0
			for _, f := range fixes {
				sum += f.FDR
			}
			avgFDR := sum / float64(len(fixes))
			fixtureBonus = (5 - avgFDR) * 1.5
		}

		total += form + ppg + fixtureBonus
	}
	return total
}

// chipsRemaining reports which of the season-half's four chips a manager
// hasn't used yet, keyed to whichever half of the season currentGW falls in.
func chipsRemaining(chips []fpl.ChipUsage, currentGW int) []string {
	all := map[string]bool{"wildcard": true, "bboost": true, "freehit": true, "3xc": true}
	used := map[string]bool{}
	for _, c := range chips {
		inSecondHalf := c.Event > halfwayGW
		if (currentGW > halfwayGW) == inSecondHalf {
			used[c.Name] = true
		}
	}
	var out []string
	for name := range all {
		if !used[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// momentum averages points over the last 5 gameweeks — a simple recent-form
// trend indicator, deliberately separate from the captain model's own form
// weighting since this operates on a manager's total score, not a player's.
func momentum(current []fpl.HistoryGameweek, currentGW int) float64 {
	sum, n := 0, 0
	for _, h := range current {
		if currentGW-5 < h.Event && h.Event <= currentGW {
			sum += h.Points
			n++
		}
	}
	if n == 0 {
		return 0.0
	}
	return float64(sum) / float64(n)
}

func teamValue(picks []fpl.Pick, playersByID map[int]*fpl.Player) float64 {
	total := 0
	for _, pick := range picks {
		if p := playersByID[pick.Element]; p != nil {
			total += p.NowCost
		}
	}
	return float64(total) / 10
}

// AnalyzeLeague is AnalyzeLeagueForTeam without a caller.
func (e *Engine) AnalyzeLeague(ctx context.Context, leagueID int) (any, error) {
	return e.AnalyzeLeagueForTeam(ctx, leagueID, nil)
}

// AnalyzeLeagueForTeam computes win probabilities for every manager on the
// first standings page, detailed rows for the top 10, and, given a team ID,
// the caller's own row.
func (e *Engine) AnalyzeLeagueForTeam(ctx context.Context, leagueID int, callerID *int) (any, error) {
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

	finishedGWs := 0
	for i := range bootstrap.Events {
		if bootstrap.Events[i].Finished {
			finishedGWs++
		}
	}
	gwsRemaining := 38 - finishedGWs

	standings := standingsData.Standings.Results
	if len(standings) == 0 {
		return &LeagueNotFound{LeagueID: leagueID, Error: "League not found or has no standings."}, nil
	}

	modelled := standings
	if len(modelled) > maxModelledManagers {
		modelled = modelled[:maxModelledManagers]
	}
	topManagers := modelled
	if len(topManagers) > maxAnalyzedManagers {
		topManagers = topManagers[:maxAnalyzedManagers]
	}
	leaderPoints := 0
	if len(topManagers) > 0 {
		leaderPoints = topManagers[0].Total
	}

	// Every modelled manager's history, for the simulation. The client
	// caches responses, so the detailed rows below reuse these fetches.
	histories := e.leagueHistories(ctx, modelled)

	teams := teamsByID(bootstrap)
	fixtureMap := buildFixtureMap(fixtures, planningGW, teams)

	managers := make([]any, 0, len(topManagers))
	// analyses parallels managers 1:1 for the *ManagerAnalysis entries only,
	// used below to compute win probabilities without re-walking the mixed
	// []any slice and type-asserting repeatedly.
	var analyses []*ManagerAnalysis

	for _, m := range topManagers {
		picks, picksErr := e.client.TeamPicks(ctx, m.Entry, currentGW)
		history := histories[m.Entry]

		if picksErr != nil || history == nil {
			managers = append(managers, &ManagerFetchError{
				ManagerName: m.PlayerName, TeamName: m.EntryName, TeamID: m.Entry,
				Rank: m.Rank, TotalPoints: m.Total, PointsFromLeader: m.Total - leaderPoints,
				WinProbability: 0.0, Error: "Could not fetch squad data",
			})
			continue
		}

		analysis := &ManagerAnalysis{
			ManagerName: m.PlayerName, TeamName: m.EntryName, TeamID: m.Entry,
			Rank: m.Rank, TotalPoints: m.Total, GWPoints: m.EventTotal,
			PointsFromLeader: m.Total - leaderPoints,
			SquadQuality:     Round(calculateSquadQuality(picks.Picks, playersByID, fixtureMap), 1),
			ChipsRemaining:   chipsRemaining(history.Chips, currentGW),
			MomentumLast5GW:  Round(momentum(history.Current, currentGW), 1),
			Bank:             Round(float64(picks.EntryHistory.Bank)/10, 1),
			TeamValue:        Round(teamValue(picks.Picks, playersByID), 1),
		}
		injured := 0
		for _, pick := range picks.Picks {
			if pick.Position > 11 {
				continue
			}
			if p := playersByID[pick.Element]; p != nil && InjuryStatuses[p.Status] {
				injured++
			}
		}
		analysis.InjuredStarters = injured

		managers = append(managers, analysis)
		analyses = append(analyses, analysis)
	}

	// The simulation, over every modelled manager in standings order.
	entries := make([]leagueSimEntry, len(modelled))
	for i, m := range modelled {
		entries[i] = leagueSimEntry{TotalPoints: m.Total}
		if h := histories[m.Entry]; h != nil {
			entries[i].NetScores = scoringWeeks(h)
			entries[i].Chips = chipsRemaining(h.Chips, currentGW)
			entries[i].HasHistory = true
		}
	}
	probs := simulateLeagueWinners(entries, gwsRemaining, uint64(leagueID)<<8|uint64(currentGW))
	probByTeam := make(map[int]float64, len(modelled))
	table := make([]LeagueWinChance, len(modelled))
	for i, m := range modelled {
		probByTeam[m.Entry] = probs[i]
		basis := "league average (history unavailable)"
		if entries[i].HasHistory {
			basis = fmt.Sprintf("own history (%d gameweeks)", len(entries[i].NetScores))
		}
		table[i] = LeagueWinChance{
			Rank: m.Rank, ManagerName: m.PlayerName, TeamName: m.EntryName, TeamID: m.Entry,
			TotalPoints: m.Total, PointsFromLeader: m.Total - leaderPoints,
			WinProbability: probs[i], Basis: basis,
		}
	}
	for _, m := range managers {
		switch v := m.(type) {
		case *ManagerAnalysis:
			v.WinProbability = probByTeam[v.TeamID]
		case *ManagerFetchError:
			v.WinProbability = probByTeam[v.TeamID]
		}
	}

	insights := buildLeagueInsights(analyses, gwsRemaining)

	standingsNote := ""
	if standingsData.Standings.HasNext || len(standings) > len(modelled) {
		standingsNote = fmt.Sprintf("This league has more managers than the first standings page. Only the top %d are modelled, and probabilities are shared among them; managers below them are not counted.", len(modelled))
	}

	return &LeagueAnalysisResult{
		LeagueID: leagueID, LeagueName: standingsData.League.Name,
		TotalManagers: len(standings), Gameweek: planningGW, GameweeksRemaining: gwsRemaining,
		AnalyzedTop: len(managers), Managers: managers, Insights: insights,
		WinProbabilityMethod: leagueWinProbabilityMethod, ManagersModelled: len(modelled),
		WinProbabilities: table, StandingsNote: standingsNote,
		Caller: leagueCallerRow(callerID, table, gwsRemaining),
	}, nil
}

// leagueHistories fetches every modelled manager's season history, eight
// at a time. A failed fetch is left out of the map, and that manager is
// simulated at the league average.
func (e *Engine) leagueHistories(ctx context.Context, managers []fpl.LeagueEntry) map[int]*fpl.TeamHistory {
	var mu sync.Mutex
	out := make(map[int]*fpl.TeamHistory, len(managers))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for _, m := range managers {
		g.Go(func() error {
			h, err := e.client.TeamHistory(gctx, m.Entry)
			if err != nil {
				return nil // best-effort, see above
			}
			mu.Lock()
			out[m.Entry] = h
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait() // every goroutine returns nil
	return out
}

// leagueCallerRow finds the caller in the probability table.
func leagueCallerRow(callerID *int, table []LeagueWinChance, gwsRemaining int) *LeagueCallerRow {
	if callerID == nil {
		return nil
	}
	for _, row := range table {
		if row.TeamID != *callerID {
			continue
		}
		gap := -row.PointsFromLeader
		required := 0.0
		if gwsRemaining > 0 {
			required = Round(float64(gap)/float64(gwsRemaining), 2)
		}
		return &LeagueCallerRow{
			TeamID: row.TeamID, Rank: row.Rank, TotalPoints: row.TotalPoints,
			PointsGapToLeader: gap, RequiredPPGOverLeader: required, WinProbability: row.WinProbability,
		}
	}
	return &LeagueCallerRow{
		TeamID: *callerID,
		Note:   "This team is not on the first page of this league's standings, so it is not modelled.",
	}
}

// buildLeagueInsights turns the numeric analysis into a short list of
// narrative observations, in the same priority order as the reference:
// favourite, race closeness, chip advantages, momentum swings, then injury
// concerns for the leading contenders.
func buildLeagueInsights(managers []*ManagerAnalysis, gwsRemaining int) []string {
	if len(managers) == 0 {
		return []string{"Could not analyze managers."}
	}

	byProb := slices.Clone(managers)
	slices.SortStableFunc(byProb, func(a, b *ManagerAnalysis) int {
		switch {
		case a.WinProbability > b.WinProbability:
			return -1
		case a.WinProbability < b.WinProbability:
			return 1
		default:
			return 0
		}
	})
	favourite := byProb[0]

	var insights []string
	insights = append(insights, fmt.Sprintf(
		"%s (%s) is the favourite at %s%% win probability — %dpts, rank %d.",
		favourite.ManagerName, favourite.TeamName, FloatStr(favourite.WinProbability),
		favourite.TotalPoints, favourite.Rank,
	))

	if len(byProb) >= 2 {
		gap := favourite.TotalPoints - byProb[1].TotalPoints
		switch {
		case gap <= 10:
			insights = append(insights, fmt.Sprintf(
				"Tight race — only %dpts separate %s and %s. With %d GWs left, anything can happen.",
				gap, favourite.ManagerName, byProb[1].ManagerName, gwsRemaining,
			))
		case gap >= 50 && gwsRemaining <= 8:
			insights = append(insights, fmt.Sprintf(
				"%s has a commanding %dpt lead with only %d GWs remaining — very difficult to overtake.",
				favourite.ManagerName, gap, gwsRemaining,
			))
		}
	}

	top5 := byProb
	if len(top5) > 5 {
		top5 = top5[:5]
	}
	for _, m := range top5 {
		if len(m.ChipsRemaining) < 3 {
			continue
		}
		names := make([]string, len(m.ChipsRemaining))
		for i, c := range m.ChipsRemaining {
			names[i] = friendlyChipName(c)
		}
		insights = append(insights, fmt.Sprintf(
			"%s still has %d chips (%s) — significant upside potential in the final stretch.",
			m.ManagerName, len(m.ChipsRemaining), strings.Join(names, ", "),
		))
	}

	var hot []*ManagerAnalysis
	for _, m := range byProb {
		if m.MomentumLast5GW >= 65 {
			hot = append(hot, m)
		}
	}
	for i, m := range hot {
		if i >= 2 {
			break
		}
		if m == favourite {
			continue
		}
		insights = append(insights, fmt.Sprintf(
			"%s is on a hot streak — averaging %spts over the last 5 GWs. Watch out.",
			m.ManagerName, FloatStr(m.MomentumLast5GW),
		))
	}

	// The analysis filters top5 to "cold" managers (momentum <= 45), takes only
	// the first such manager, and checks whether *that* one is the
	// favourite. Since favourite is always by_prob[0] — the highest-ranked
	// element of any list that preserves by_prob order — it is necessarily
	// first among any subset it belongs to. So the whole dance reduces to
	// checking the favourite's own momentum directly.
	if favourite.MomentumLast5GW <= 45 {
		insights = append(insights, fmt.Sprintf(
			"Warning: %s leads but has cooled off recently (%spts avg last 5 GWs). Door is open for challengers.",
			favourite.ManagerName, FloatStr(favourite.MomentumLast5GW),
		))
	}

	top3 := byProb
	if len(top3) > 3 {
		top3 = top3[:3]
	}
	for _, m := range top3 {
		if m.InjuredStarters >= 2 {
			insights = append(insights, fmt.Sprintf(
				"%s has %d injured starters — may need to burn transfers or take hits.",
				m.ManagerName, m.InjuredStarters,
			))
		}
	}

	return insights
}

func friendlyChipName(c string) string {
	switch c {
	case "3xc":
		return "Triple Captain"
	case "bboost":
		return "Bench Boost"
	case "freehit":
		return "Free Hit"
	default:
		return c
	}
}
