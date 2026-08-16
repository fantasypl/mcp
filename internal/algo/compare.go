package algo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Fuzzy-matches 2-4 player names against web_name (and, failing that, full
// name), then builds a rich head-to-head profile for each: captain score
// (reusing captain.go's scoring, so the number here is directly comparable to
// a captain_pick result), home/away form split from element-summary history,
// upcoming fixtures, and a verdict recommending the best pick.

// compareResultKind selects which of the three distinct response shapes
// CompareResult.MarshalJSON renders. A struct tag can't express "this key is
// present, with an empty list, in branch B but absent entirely in branch A" —
// omitempty conflates "empty" and "unset" — so the shape is picked explicitly
// instead of inferred from zero values.
type compareResultKind int

const (
	compareKindError   compareResultKind = iota // {"error": ...}
	compareKindNoMatch                          // {"error", "details", "matched"}
	compareKindSuccess                          // {"gameweek", "gameweeks_ahead", "players", "verdict"}
)

// CompareResult is the compare_players output.
type CompareResult struct {
	kind compareResultKind

	Error   string
	Details []string
	Matched []MatchedQuery

	Gameweek       int
	GameweeksAhead int
	Players        []PlayerProfile
	Verdict        string
}

func compareError(msg string) *CompareResult {
	return &CompareResult{kind: compareKindError, Error: msg}
}

func compareNoMatch(msg string, details []string, matched []MatchedQuery) *CompareResult {
	return &CompareResult{kind: compareKindNoMatch, Error: msg, Details: details, Matched: matched}
}

func compareSuccess(gw, gameweeksAhead int, players []PlayerProfile, verdict string) *CompareResult {
	return &CompareResult{kind: compareKindSuccess, Gameweek: gw, GameweeksAhead: gameweeksAhead, Players: players, Verdict: verdict}
}

func (c CompareResult) MarshalJSON() ([]byte, error) {
	switch c.kind {
	case compareKindNoMatch:
		return json.Marshal(struct {
			Error   string         `json:"error"`
			Details []string       `json:"details"`
			Matched []MatchedQuery `json:"matched"`
		}{c.Error, c.Details, c.Matched})
	case compareKindSuccess:
		return json.Marshal(struct {
			Gameweek       int             `json:"gameweek"`
			GameweeksAhead int             `json:"gameweeks_ahead"`
			Players        []PlayerProfile `json:"players"`
			Verdict        string          `json:"verdict"`
		}{c.Gameweek, c.GameweeksAhead, c.Players, c.Verdict})
	default:
		return json.Marshal(struct {
			Error string `json:"error"`
		}{c.Error})
	}
}

// MatchedQuery is one successfully-matched name in a "could not match all
// names" error response.
type MatchedQuery struct {
	Query       string `json:"query"`
	MatchedName string `json:"matched_name"`
}

// UpcomingFixture is one gameweek's fixture (or blank) for a compared
// player's team. FDR/IsHome are nil for a blank gameweek and therefore render
// as JSON null rather than being omitted.
type UpcomingFixture struct {
	Gameweek int      `json:"gameweek"`
	Opponent string   `json:"opponent"`
	FDR      *float64 `json:"fdr"`
	IsHome   *bool    `json:"is_home"`
}

// PlayerProfile is one player's entry in a comparison. Every field below is a
// fixed key in the profile response — including the nullable ones — so none
// use omitempty; a null value renders as JSON null rather than an absent key.
type PlayerProfile struct {
	Query                      string            `json:"query"`
	MatchConfidence            string            `json:"match_confidence"`
	Name                       string            `json:"name"`
	FullName                   string            `json:"full_name"`
	ID                         int               `json:"id"`
	Team                       string            `json:"team"`
	TeamFullName               string            `json:"team_full_name"`
	Position                   string            `json:"position"`
	Cost                       float64           `json:"cost"`
	OwnershipPct               float64           `json:"ownership_pct"`
	Form                       float64           `json:"form"`
	HomeForm                   float64           `json:"home_form"`
	AwayForm                   float64           `json:"away_form"`
	HomeAwayInsight            string            `json:"home_away_insight"`
	PointsPerGame              float64           `json:"points_per_game"`
	EPNext                     float64           `json:"ep_next"`
	TotalPoints                int               `json:"total_points"`
	XGPer90                    float64           `json:"xg_per_90"`
	XAPer90                    float64           `json:"xa_per_90"`
	XGCPer90                   *float64          `json:"xgc_per_90"`
	DefensiveContributionPer90 *float64          `json:"defensive_contribution_per_90"`
	ICTIndex                   float64           `json:"ict_index"`
	CaptainScore               float64           `json:"captain_score"`
	ValueScore                 float64           `json:"value_score"`
	ConsistencyScore           float64           `json:"consistency_score"`
	NetTransfersThisGW         int               `json:"net_transfers_this_gw"`
	TransferPressure           string            `json:"transfer_pressure"`
	Status                     string            `json:"status"`
	ChanceOfPlaying            *int              `json:"chance_of_playing"`
	News                       *PlayerNews       `json:"news"`
	UpcomingFixtures           []UpcomingFixture `json:"upcoming_fixtures"`
	BlankGameweeks             []int             `json:"blank_gameweeks"`

	// avgFDR and gameweeksAhead are internal: JSON renders avg_fdr under a
	// key whose name embeds gameweeks_ahead (e.g. "avg_fdr_next_5_gws"), which
	// a static json tag can't express. MarshalJSON below injects it.
	avgFDR         *float64
	gameweeksAhead int
}

// MarshalJSON adds the dynamic "avg_fdr_next_{N}_gws" key that a struct tag
// can't express, on top of every fixed field above.
func (p PlayerProfile) MarshalJSON() ([]byte, error) {
	type alias PlayerProfile
	b, err := json.Marshal(alias(p))
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	avgVal, err := json.Marshal(p.avgFDR)
	if err != nil {
		return nil, err
	}
	m[fmt.Sprintf("avg_fdr_next_%d_gws", p.gameweeksAhead)] = avgVal
	return json.Marshal(m)
}

// calcHomeAwayForm computes average points from the last 5 home and last 5
// away games played (minutes > 0).
func calcHomeAwayForm(summary *fpl.PlayerSummary) (homeForm, awayForm float64, insight string) {
	var homeGames, awayGames []fpl.PlayerHistoryEntry
	for _, m := range summary.History {
		if m.Minutes <= 0 {
			continue
		}
		if m.WasHome {
			homeGames = append(homeGames, m)
		} else {
			awayGames = append(awayGames, m)
		}
	}

	homeForm = avgTotalPoints(lastN(homeGames, 5))
	awayForm = avgTotalPoints(lastN(awayGames, 5))

	diff := homeForm - awayForm
	switch {
	case math.Abs(diff) < 1.0:
		insight = fmt.Sprintf("Similar home and away form (H: %s / A: %s)", FloatStr(homeForm), FloatStr(awayForm))
	case diff > 0:
		insight = fmt.Sprintf("Stronger at home (avg %s vs %s away)", FloatStr(homeForm), FloatStr(awayForm))
	default:
		insight = fmt.Sprintf("Stronger away (avg %s vs %s at home)", FloatStr(awayForm), FloatStr(homeForm))
	}
	return homeForm, awayForm, insight
}

func lastN(s []fpl.PlayerHistoryEntry, n int) []fpl.PlayerHistoryEntry {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func avgTotalPoints(games []fpl.PlayerHistoryEntry) float64 {
	if len(games) == 0 {
		return 0.0
	}
	sum := 0
	for _, g := range games {
		sum += g.TotalPoints
	}
	return Round(float64(sum)/float64(len(games)), 1)
}

// fuzzyMatchPlayer finds the best fuzzy name match for a player.
//
// Priority: exact web_name match, web_name prefix, web_name substring, full
// name substring. Within a tier, ties go to the player with the most total
// points — and among equal-points players, to whichever is *first*
// in elements order, not the last.
func fuzzyMatchPlayer(name string, elements []fpl.Player) (*fpl.Player, string, bool) {
	query := strings.ToLower(strings.TrimSpace(name))
	if query == "" {
		return nil, "", false
	}

	var exact, startsWith, contains, fullNameContains []*fpl.Player
	for i := range elements {
		p := &elements[i]
		web := strings.ToLower(p.WebName)
		full := strings.ToLower(p.FirstName + " " + p.SecondName)

		switch {
		case web == query:
			exact = append(exact, p)
		case strings.HasPrefix(web, query):
			startsWith = append(startsWith, p)
		case strings.Contains(web, query):
			contains = append(contains, p)
		case strings.Contains(full, query):
			fullNameContains = append(fullNameContains, p)
		}
	}

	tiers := [][]*fpl.Player{exact, startsWith, contains, fullNameContains}
	tierNames := [...]string{"exact", "starts_with", "contains", "full_name"}
	for i, group := range tiers {
		if len(group) == 0 {
			continue
		}
		best := group[0]
		for _, p := range group[1:] {
			if p.TotalPoints > best.TotalPoints {
				best = p
			}
		}
		return best, tierNames[i], true
	}
	return nil, "", false
}

// buildUpcomingFixtures reuses buildFixtureMap (captain.go) per gameweek —
// so the FDR shown here is the
// same team-strength-blended FDR captain scoring uses, not the raw FPL 1-5.
func buildUpcomingFixtures(teamID int, fixtures []fpl.Fixture, nextGW, gameweeksAhead int, teams map[int]*fpl.Team) []UpcomingFixture {
	upcoming := make([]UpcomingFixture, 0, gameweeksAhead)
	for gw := nextGW; gw < nextGW+gameweeksAhead; gw++ {
		fixtureMap := buildFixtureMap(fixtures, gw, teams)
		gwFixtures := fixtureMap[teamID]
		if len(gwFixtures) == 0 {
			upcoming = append(upcoming, UpcomingFixture{Gameweek: gw, Opponent: "BLANK"})
			continue
		}
		for _, f := range gwFixtures {
			venue := "A"
			if f.IsHome {
				venue = "H"
			}
			fdr := f.FDR
			isHome := f.IsHome
			upcoming = append(upcoming, UpcomingFixture{
				Gameweek: gw,
				Opponent: fmt.Sprintf("%s(%s)", shortName(teams[f.Opponent]), venue),
				FDR:      &fdr,
				IsHome:   &isHome,
			})
		}
	}
	return upcoming
}

func transferPressure(net int) string {
	switch {
	case net > 50_000:
		return "Rising"
	case net < -50_000:
		return "Falling"
	default:
		return "Stable"
	}
}

// buildVerdict picks a winner among the compared players.
//
// Every "pick the best/second-best/runner-up" step below keeps the first
// player encountered on a tie.
func buildVerdict(profiles []PlayerProfile) string {
	if len(profiles) == 0 {
		return "No players to compare."
	}

	best := profiles[0]
	for _, p := range profiles[1:] {
		if p.CaptainScore > best.CaptainScore {
			best = p
		}
	}
	name := best.Name

	var others []PlayerProfile
	for _, p := range profiles {
		if p.Name != name {
			others = append(others, p)
		}
	}

	var reasons []string

	if len(others) > 0 {
		secondBest := others[0]
		for _, p := range others[1:] {
			if p.CaptainScore > secondBest.CaptainScore {
				secondBest = p
			}
		}
		margin := best.CaptainScore - secondBest.CaptainScore
		switch {
		case margin > 3:
			reasons = append(reasons, fmt.Sprintf("significantly higher captain score (%.1f vs %.1f)", best.CaptainScore, secondBest.CaptainScore))
		case margin > 0:
			reasons = append(reasons, fmt.Sprintf("higher captain score (%.1f vs %.1f)", best.CaptainScore, secondBest.CaptainScore))
		}
	}

	if best.Form >= 5 {
		reasons = append(reasons, fmt.Sprintf("strong recent form (%.1f)", best.Form))
	}

	var upcomingWithFDR []UpcomingFixture
	for _, f := range best.UpcomingFixtures {
		if f.FDR != nil {
			upcomingWithFDR = append(upcomingWithFDR, f)
		}
	}
	if len(upcomingWithFDR) > 0 {
		sum := 0.0
		for _, f := range upcomingWithFDR {
			sum += *f.FDR
		}
		avgFDR := sum / float64(len(upcomingWithFDR))
		switch {
		case avgFDR <= 2.5:
			reasons = append(reasons, "excellent upcoming fixtures")
		case avgFDR <= 3.0:
			reasons = append(reasons, "favourable upcoming fixtures")
		}
	}

	if best.ValueScore > 0 {
		bestValue := profiles[0]
		for _, p := range profiles[1:] {
			if p.ValueScore > bestValue.ValueScore {
				bestValue = p
			}
		}
		if bestValue.Name == name {
			reasons = append(reasons, fmt.Sprintf("best value at %sm", FloatStr(best.Cost)))
		}
	}

	if best.XGPer90 > 0.3 {
		reasons = append(reasons, fmt.Sprintf("strong xG/90 (%.2f)", best.XGPer90))
	}

	reasonStr := "marginally edges out the competition on overall score"
	if len(reasons) > 0 {
		reasonStr = strings.Join(reasons, ", ")
	}

	if len(others) > 0 {
		runnerUp := others[0]
		for _, p := range others[1:] {
			if p.CaptainScore > runnerUp.CaptainScore {
				runnerUp = p
			}
		}
		gap := best.CaptainScore - runnerUp.CaptainScore
		if gap < 1 {
			return fmt.Sprintf(
				"%s narrowly edges %s — %s. It's close though; %s is a viable alternative.",
				name, runnerUp.Name, reasonStr, runnerUp.Name,
			)
		}
	}

	return fmt.Sprintf("%s is the clear pick — %s.", name, reasonStr)
}

// ComparePlayers runs a 2-4 player head-to-head comparison.
//
// gameweeksAhead <= 0 defaults to 5; any value is then clamped to [1, 10].
func (e *Engine) ComparePlayers(ctx context.Context, playerNames []string, gameweeksAhead int) (*CompareResult, error) {
	if len(playerNames) < 2 {
		return compareError("Please provide at least 2 player names to compare."), nil
	}
	if len(playerNames) > 4 {
		return compareError("Please provide at most 4 player names to compare."), nil
	}

	if gameweeksAhead <= 0 {
		gameweeksAhead = 5
	}
	if gameweeksAhead > 10 {
		gameweeksAhead = 10
	}

	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	fixtures, err := e.client.Fixtures(ctx)
	if err != nil {
		return nil, err
	}

	nextGW := bootstrap.NextGameweek()
	teams := teamsByID(bootstrap)
	fixtureMap := buildFixtureMap(fixtures, nextGW, teams)

	type matchedPlayer struct {
		query  string
		player *fpl.Player
		tier   string
	}
	var matched []matchedPlayer
	var errs []string
	for _, name := range playerNames {
		p, tier, ok := fuzzyMatchPlayer(name, bootstrap.Elements)
		if !ok {
			errs = append(errs, fmt.Sprintf("No match found for '%s'.", name))
			continue
		}
		matched = append(matched, matchedPlayer{query: name, player: p, tier: tier})
	}

	if len(errs) > 0 {
		matchedOut := make([]MatchedQuery, 0, len(matched))
		for _, m := range matched {
			matchedOut = append(matchedOut, MatchedQuery{Query: m.query, MatchedName: m.player.WebName})
		}
		return compareNoMatch("Could not match all player names.", errs, matchedOut), nil
	}

	summaries := make(map[int]*fpl.PlayerSummary, len(matched))
	for _, m := range matched {
		s, err := e.client.PlayerSummary(ctx, m.player.ID)
		if err != nil {
			return nil, err
		}
		summaries[m.player.ID] = s
	}

	profiles := make([]PlayerProfile, 0, len(matched))
	for _, m := range matched {
		p := m.player
		team := teams[p.Team]
		playerFixtures := fixtureMap[p.Team]

		captainScore := e.scorePlayer(p, playerFixtures)

		nineties := 0.0
		if p.Minutes > 0 {
			nineties = float64(p.Minutes) / 90.0
		}
		xg := p.ExpectedGoals.Float()
		xa := p.ExpectedAssists.Float()
		xgPer90, xaPer90 := 0.0, 0.0
		if nineties > 0 {
			xgPer90 = Round(xg/nineties, 3)
			xaPer90 = Round(xa/nineties, 3)
		}

		form := p.Form.Float()
		ppg := p.PointsPerGame.Float()
		epNext := p.EPNext.Float()
		totalPoints := p.TotalPoints
		cost := float64(p.NowCost) / 10
		ownership := p.SelectedByPercent.Float()
		ict := p.ICTIndex.Float()

		var xgcPer90 *float64
		if p.ElementType == 1 || p.ElementType == 2 {
			v := p.ExpectedGoalsConcededPer90.Float()
			xgcPer90 = &v
		}

		var defContribPer90 *float64
		if p.ElementType == 2 || p.ElementType == 3 || p.ElementType == 4 {
			v := p.DefensiveContributionPer90.Float()
			defContribPer90 = &v
		}

		netTransfers := p.TransfersInEvent - p.TransfersOutEvent

		valueScore := 0.0
		if cost > 0 {
			valueScore = Round(float64(totalPoints)/cost, 2)
		}

		formPPGRatio := 0.0
		if ppg > 0 {
			formPPGRatio = form / ppg
		}
		consistencyScore := 0.0
		if p.Starts > 0 && totalPoints > 0 {
			consistencyScore = Round(math.Min(10.0, formPPGRatio*ppg), 1)
		}

		upcoming := buildUpcomingFixtures(p.Team, fixtures, nextGW, gameweeksAhead, teams)
		blankGWs := make([]int, 0, gameweeksAhead)
		var fdrSum float64
		var fdrCount int
		for _, f := range upcoming {
			if f.FDR != nil {
				fdrSum += *f.FDR
				fdrCount++
			} else {
				blankGWs = append(blankGWs, f.Gameweek)
			}
		}
		var avgFDR *float64
		if fdrCount > 0 {
			v := Round(fdrSum/float64(fdrCount), 2)
			avgFDR = &v
		}

		homeForm, awayForm, haInsight := calcHomeAwayForm(summaries[p.ID])

		status := p.Status
		if status == "" {
			status = "a"
		}

		profiles = append(profiles, PlayerProfile{
			Query:                      m.query,
			MatchConfidence:            m.tier,
			Name:                       p.WebName,
			FullName:                   p.FirstName + " " + p.SecondName,
			ID:                         p.ID,
			Team:                       shortName(team),
			TeamFullName:               fullName(team),
			Position:                   Position(p.ElementType),
			Cost:                       cost,
			OwnershipPct:               ownership,
			Form:                       form,
			HomeForm:                   homeForm,
			AwayForm:                   awayForm,
			HomeAwayInsight:            haInsight,
			PointsPerGame:              ppg,
			EPNext:                     epNext,
			TotalPoints:                totalPoints,
			XGPer90:                    xgPer90,
			XAPer90:                    xaPer90,
			XGCPer90:                   xgcPer90,
			DefensiveContributionPer90: defContribPer90,
			ICTIndex:                   ict,
			CaptainScore:               captainScore,
			ValueScore:                 valueScore,
			ConsistencyScore:           consistencyScore,
			NetTransfersThisGW:         netTransfers,
			TransferPressure:           transferPressure(netTransfers),
			Status:                     status,
			ChanceOfPlaying:            p.ChanceOfPlayingNextRound,
			News:                       GetPlayerNews(p, e.Now()),
			UpcomingFixtures:           upcoming,
			BlankGameweeks:             blankGWs,
			avgFDR:                     avgFDR,
			gameweeksAhead:             gameweeksAhead,
		})
	}

	return compareSuccess(nextGW, gameweeksAhead, profiles, buildVerdict(profiles)), nil
}
