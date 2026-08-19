package algo

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Chip strategy answers "when should I use my remaining chips?" — and,
// critically, evaluates them as a *sequence* rather than independently. The
// single most valuable pattern in FPL chip strategy is Wildcard one gameweek
// before a mega Double Gameweek Bench Boost: the wildcard rebuilds the whole
// squad specifically so the bench boost has 15 players worth boosting, not
// just 11. Scoring each chip in isolation would miss that combo entirely, so
// Wildcard's score is computed *after*, and *aware of*, the best Bench Boost
// and Free Hit gameweeks.
//
// This also folds in dgw_intel's community-sourced DGW/BGW predictions,
// which routinely know about a rescheduled fixture before the FPL API
// reflects it — the gap between "a match got postponed" and "the makeup date
// is confirmed" can be weeks, and a manager holding a chip for that window
// needs the earlier signal.

// scanWindow is how many gameweeks ahead this scans for chip opportunities.
const chipScanWindow = 10

// allChips is every chip code the FPL API uses.
var allChips = []string{"wildcard", "bboost", "freehit", "3xc"}

var chipDisplay = map[string]string{
	"bboost":   "Bench Boost",
	"3xc":      "Triple Captain",
	"freehit":  "Free Hit",
	"wildcard": "Wildcard",
}

func displayName(code string) string {
	if d, ok := chipDisplay[code]; ok {
		return d
	}
	return code
}

// countDGWTeams counts teams with 2+ fixtures in gameweek gw — a confirmed
// double gameweek.
func countDGWTeams(fixtures []fpl.Fixture, gw int) int {
	counts := teamFixtureCounts(fixtures, gw)
	n := 0
	for _, c := range counts {
		if c >= 2 {
			n++
		}
	}
	return n
}

func teamFixtureCounts(fixtures []fpl.Fixture, gw int) map[int]int {
	counts := make(map[int]int)
	for i := range fixtures {
		f := &fixtures[i]
		if !f.InGameweek(gw) {
			continue
		}
		counts[f.TeamH]++
		counts[f.TeamA]++
	}
	return counts
}

// pendingFixture is one of a team's unscheduled (postponed) fixtures.
type pendingFixture struct {
	Opponent  int
	IsHome    bool
	FixtureID int
}

// predictDGWTeams finds teams with postponed fixtures (event=null, not yet
// finished) — the leading indicator that a future gameweek will become a
// DGW once the makeup date is confirmed. teamOrder preserves the order teams
// were first encountered scanning fixtures, matching the insertion order a
// The response map is built this way — several downstream displays
// iterate this order rather than sorting by team ID.
func predictDGWTeams(fixtures []fpl.Fixture) (pending map[int][]pendingFixture, teamOrder []int) {
	pending = make(map[int][]pendingFixture)
	seen := make(map[int]bool)

	addTeam := func(tid int) {
		if !seen[tid] {
			seen[tid] = true
			teamOrder = append(teamOrder, tid)
		}
	}

	for i := range fixtures {
		f := &fixtures[i]
		if _, ok := f.EventOf(); ok || f.Finished {
			continue
		}
		pending[f.TeamH] = append(pending[f.TeamH], pendingFixture{Opponent: f.TeamA, IsHome: true, FixtureID: f.ID})
		addTeam(f.TeamH)
		pending[f.TeamA] = append(pending[f.TeamA], pendingFixture{Opponent: f.TeamH, IsHome: false, FixtureID: f.ID})
		addTeam(f.TeamA)
	}
	return pending, teamOrder
}

// estimateLikelyDGWGameweeks guesses which upcoming gameweeks are likely to
// absorb a team's postponed fixture: a team with a pending game that
// currently has only 1 fixture in a scanned gameweek is a candidate — 2
// would already be a confirmed DGW, 0 means the postponement can't land
// there without becoming a 2-fixture week either.
func estimateLikelyDGWGameweeks(fixtures []fpl.Fixture, scanGWs []int, teamsWithPending map[int][]pendingFixture, teamOrder []int) map[int][]int {
	if len(teamsWithPending) == 0 {
		return nil
	}

	out := make(map[int][]int)
	for _, gw := range scanGWs {
		counts := teamFixtureCounts(fixtures, gw)
		var likely []int
		for _, tid := range teamOrder {
			if counts[tid] == 1 {
				likely = append(likely, tid)
			}
		}
		if len(likely) > 0 {
			out[gw] = likely
		}
	}
	return out
}

func countBlankingTeams(fixtures []fpl.Fixture, gw int, allTeamIDs map[int]bool) int {
	playing := make(map[int]bool)
	for i := range fixtures {
		f := &fixtures[i]
		if !f.InGameweek(gw) {
			continue
		}
		playing[f.TeamH] = true
		playing[f.TeamA] = true
	}
	n := 0
	for tid := range allTeamIDs {
		if !playing[tid] {
			n++
		}
	}
	return n
}

func avgFDRForGW(fixtures []fpl.Fixture, gw int) float64 {
	sum, n := 0.0, 0
	for i := range fixtures {
		f := &fixtures[i]
		if !f.InGameweek(gw) {
			continue
		}
		sum += float64(f.TeamHDifficulty) + float64(f.TeamADifficulty)
		n += 2
	}
	if n == 0 {
		return 3.0
	}
	return Round(sum/float64(n), 2)
}

func gwFixtureCount(fixtures []fpl.Fixture, gw int) int {
	n := 0
	for i := range fixtures {
		if fixtures[i].InGameweek(gw) {
			n++
		}
	}
	return n
}

// gwChipStats is the per-gameweek context every chip's scoring function reads.
type gwChipStats struct {
	dgwTeams          int
	predictedDGWTeams []int // team IDs, in scan order
	blankTeams        int
	avgFDR            float64
	fixtureCount      int
	fixtureMap        map[int][]TeamFixture
}

func inIntSlice(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// scoreBBForGW scores a gameweek for Bench Boost. With a Wildcard still
// available, the bench will be rebuilt specifically for this gameweek, so
// the score is based on the gameweek's raw potential (DGW teams, fixture
// count) rather than the *current* bench, which is about to be replaced.
func scoreBBForGW(stats *gwChipStats, benchPlayers []*fpl.Player, hasWildcard bool) float64 {
	score := 0.0
	if hasWildcard {
		score += float64(stats.dgwTeams) * 8.0
		score += float64(len(stats.predictedDGWTeams)) * 4.0
		score += float64(stats.fixtureCount) * 1.5
		score += (5 - stats.avgFDR) * 2.0
		return score
	}

	for _, p := range benchPlayers {
		fixes := stats.fixtureMap[p.Team]
		if len(fixes) > 0 {
			fixtureBonus := float64(len(fixes)) * 3.0
			sum := 0.0
			for _, f := range fixes {
				sum += f.FDR
			}
			fdrScore := (5 - sum/float64(len(fixes))) * 2.0
			score += fixtureBonus + fdrScore
		}
		if inIntSlice(stats.predictedDGWTeams, p.Team) {
			score += 2.5
		}
	}
	score += float64(stats.dgwTeams) * 3.0
	score += float64(len(stats.predictedDGWTeams)) * 2.0
	return score
}

// scoreFHForGW scores a gameweek for Free Hit — dominated by how many teams
// are blanking, since that's exactly when a squad full of non-playing
// players needs a one-week total rebuild.
func scoreFHForGW(stats *gwChipStats) float64 {
	score := 0.0
	switch {
	case stats.blankTeams >= 10:
		score += float64(stats.blankTeams) * 10.0
	case stats.blankTeams >= 4:
		score += float64(stats.blankTeams) * 7.0
	default:
		score += float64(stats.blankTeams) * 3.0
	}

	score += float64(stats.dgwTeams) * 3.0
	score += float64(len(stats.predictedDGWTeams)) * 2.0

	var fdrs []float64
	for _, fixes := range stats.fixtureMap {
		for _, f := range fixes {
			fdrs = append(fdrs, f.FDR)
		}
	}
	if len(fdrs) > 0 {
		sum := 0.0
		for _, f := range fdrs {
			sum += (f - 3) * (f - 3)
		}
		score += (sum / float64(len(fdrs))) * 1.5
	}
	return score
}

// scoreTCForGW scores a gameweek for Triple Captain: the ceiling is whoever
// the best captain option is, doubled again if they have a double gameweek
// — a DGW captain effectively triples rather than doubles their output.
func scoreTCForGW(e *Engine, stats *gwChipStats, allPlayers []fpl.Player) (float64, *fpl.Player) {
	topScore := -999.0
	var topPlayer *fpl.Player

	for i := range allPlayers {
		p := &allPlayers[i]
		fixes := stats.fixtureMap[p.Team]
		captainScore := e.scorePlayer(p, fixes)

		switch {
		case len(fixes) >= 2:
			captainScore *= 2.0
		case inIntSlice(stats.predictedDGWTeams, p.Team):
			captainScore *= 1.6
		}

		if captainScore > topScore {
			topScore = captainScore
			topPlayer = p
		}
	}

	dgwMult := 1.0
	if stats.dgwTeams > 0 {
		dgwMult += float64(stats.dgwTeams) * 0.15
	}
	if len(stats.predictedDGWTeams) > 0 {
		dgwMult += float64(len(stats.predictedDGWTeams)) * 0.1
	}
	return topScore * dgwMult, topPlayer
}

// scoreWCForGW scores a gameweek for Wildcard. Its value is overwhelmingly
// strategic: enabling the WC->BB combo dominates every other consideration,
// which is why this needs to already know the best Bench Boost and Free Hit
// gameweeks before it can score anything.
func scoreWCForGW(stats *gwChipStats, squadPlayers []*fpl.Player, bestBBGW, bestFHGW *int, chipsRemaining map[string]bool, gw int, gwStatsByGW map[int]*gwChipStats) float64 {
	score := 0.0

	if chipsRemaining["bboost"] && bestBBGW != nil {
		switch gw {
		case *bestBBGW - 1:
			bbStats := gwStatsByGW[*bestBBGW]
			dgwTeams := 0
			if bbStats != nil {
				dgwTeams = bbStats.dgwTeams + len(bbStats.predictedDGWTeams)
			}
			score += 50.0 + float64(dgwTeams)*5.0
		case *bestBBGW - 2:
			score += 25.0
		}
	}

	if chipsRemaining["freehit"] && bestFHGW != nil {
		notBBCombo := bestBBGW == nil || gw != *bestBBGW-1
		if gw == *bestFHGW-1 && notBBCombo {
			score += 15.0
		}
	}

	injuredCount := 0
	for _, p := range squadPlayers {
		if InjuryStatuses[p.Status] {
			injuredCount++
		}
	}
	score += float64(injuredCount) * 3.0

	badFormCount := 0
	for _, p := range squadPlayers {
		form := p.Form.Float()
		avgFDR := 3.0
		if fixes := stats.fixtureMap[p.Team]; len(fixes) > 0 {
			sum := 0.0
			for _, f := range fixes {
				sum += f.FDR
			}
			avgFDR = sum / float64(len(fixes))
		}
		if form <= 3.0 && avgFDR >= 3.5 {
			badFormCount++
		}
	}
	if badFormCount >= 4 {
		score += float64(badFormCount) * 3.0
	}

	squadTeamIDs := make(map[int]bool, len(squadPlayers))
	for _, p := range squadPlayers {
		squadTeamIDs[p.Team] = true
	}
	var nonSquadFDRs []float64
	for tid, fixes := range stats.fixtureMap {
		if squadTeamIDs[tid] {
			continue
		}
		for _, f := range fixes {
			nonSquadFDRs = append(nonSquadFDRs, f.FDR)
		}
	}
	if len(nonSquadFDRs) > 0 {
		sum := 0.0
		for _, f := range nonSquadFDRs {
			sum += f
		}
		score += (5 - sum/float64(len(nonSquadFDRs))) * 2.0
	}

	return score
}

// chipAssignment is one chip's outcome from the optimizer: which gameweek,
// its score, and — for Triple Captain only — the player it would be used on.
type chipAssignment struct {
	gw       int
	score    float64
	tcPlayer *fpl.Player
}

// findOptimalChipAssignment searches for the gameweek-to-chip assignment
// that maximises total value, under one hard constraint: no two chips share
// a gameweek. With up to 4 chips and a handful of strong per-chip candidate
// gameweeks each, brute-forcing every valid combination is cheap and exact —
// no need for anything cleverer.
//
// chips is iterated in a fixed canonical order (allChips' declaration order)
// rather than whatever order a Go map would give. This fixed order makes
// tie-breaking deterministic across runs. In practice this only matters when two full assignments tie
// exactly on total score, which needs several independent float sums to
// collide bit-for-bit — essentially never happens with real fixture data.
func findOptimalChipAssignment(chipsRemaining map[string]bool, scanGWs []int, gwStats map[int]*gwChipStats, benchPlayers, squadPlayers []*fpl.Player, allPlayers []fpl.Player, e *Engine) map[string]chipAssignment {
	hasWildcard := chipsRemaining["wildcard"]

	bbScores := map[int]float64{}
	fhScores := map[int]float64{}
	tcResults := map[int]chipAssignment{}

	for _, gw := range scanGWs {
		stats := gwStats[gw]
		if chipsRemaining["bboost"] {
			bbScores[gw] = scoreBBForGW(stats, benchPlayers, hasWildcard)
		}
		if chipsRemaining["freehit"] {
			fhScores[gw] = scoreFHForGW(stats)
		}
		if chipsRemaining["3xc"] {
			score, player := scoreTCForGW(e, stats, allPlayers)
			tcResults[gw] = chipAssignment{gw: gw, score: score, tcPlayer: player}
		}
	}

	bestBBGW := bestGWByScore(bbScores, scanGWs)
	bestFHGW := bestGWByScore(fhScores, scanGWs)

	wcScores := map[int]float64{}
	if chipsRemaining["wildcard"] {
		for _, gw := range scanGWs {
			wcScores[gw] = scoreWCForGW(gwStats[gw], squadPlayers, bestBBGW, bestFHGW, chipsRemaining, gw, gwStats)
		}
	}

	var chips []string
	for _, c := range allChips {
		if chipsRemaining[c] {
			chips = append(chips, c)
		}
	}

	scoresFor := func(chip string) map[int]float64 {
		switch chip {
		case "bboost":
			return bbScores
		case "freehit":
			return fhScores
		case "wildcard":
			return wcScores
		case "3xc":
			out := make(map[int]float64, len(tcResults))
			for gw, r := range tcResults {
				out[gw] = r.score
			}
			return out
		}
		return nil
	}

	if len(chips) <= 1 {
		result := make(map[string]chipAssignment)
		for _, chip := range chips {
			if chip == "3xc" {
				if bestGW := bestGWByScore(scoresFor(chip), scanGWs); bestGW != nil {
					result[chip] = tcResults[*bestGW]
				}
				continue
			}
			scores := scoresFor(chip)
			if bestGW := bestGWByScore(scores, scanGWs); bestGW != nil {
				result[chip] = chipAssignment{gw: *bestGW, score: scores[*bestGW]}
			}
		}
		return result
	}

	// Multiple chips: cap the search space to each chip's top-5 candidate
	// gameweeks (by its own independent score) and brute-force every
	// combination that doesn't double-book a gameweek — at most 5^4 = 625
	// combinations, negligible.
	candidates := make(map[string][]int, len(chips))
	for _, chip := range chips {
		scores := scoresFor(chip)
		// Built by walking scanGWs (ascending), not by ranging over the
		// scores map — Go map iteration is randomised, and the stable sort
		// below only preserves tie-order correctly if its *input* order is
		// deterministic. Sorting the score keys, which
		// iterates in dict insertion order — itself scan_gws order, since
		// every score map here was built by looping scan_gws.
		gws := make([]int, 0, len(scores))
		for _, gw := range scanGWs {
			if _, ok := scores[gw]; ok {
				gws = append(gws, gw)
			}
		}
		sort.SliceStable(gws, func(i, j int) bool { return scores[gws[i]] > scores[gws[j]] })
		if len(gws) > 5 {
			gws = gws[:5]
		}
		candidates[chip] = gws
	}

	best := make(map[string]chipAssignment)
	bestTotal := -999.0
	current := make(map[string]chipAssignment, len(chips))
	usedGWs := make(map[int]bool, len(chips))

	var search func(idx int, runningTotal float64)
	search = func(idx int, runningTotal float64) {
		if idx == len(chips) {
			if runningTotal > bestTotal {
				bestTotal = runningTotal
				best = make(map[string]chipAssignment, len(current))
				for k, v := range current {
					best[k] = v
				}
			}
			return
		}
		chip := chips[idx]
		scores := scoresFor(chip)
		for _, gw := range candidates[chip] {
			if usedGWs[gw] {
				continue
			}
			var a chipAssignment
			if chip == "3xc" {
				a = tcResults[gw]
			} else {
				a = chipAssignment{gw: gw, score: scores[gw]}
			}
			current[chip] = a
			usedGWs[gw] = true
			search(idx+1, runningTotal+a.score)
			delete(usedGWs, gw)
			delete(current, chip)
		}
	}
	search(0, 0.0)

	return best
}

// ---------------------------------------------------------------------------
// Output shapes. Each chip type's recommendation has a genuinely different
// field set for each chip (Triple Captain has a suggested_captain, Wildcard
// has squad_issues, Free Hit's gw_details carries an extra blank_teams
// field none of the others do) — modelled here as four distinct structs
// rather than one struct with optional fields, for the same reason
// TransferSuggestions returns `any`: a shared struct with omitempty would
// have nowhere honest to put "this field is a real 0" versus "this field
// was never part of this shape."
// ---------------------------------------------------------------------------

type ChipUsageDisplay struct {
	Chip     string `json:"chip"`
	Gameweek int    `json:"gameweek"`
}

// NoChipsRemainingResult is returned once every chip has been used —
// entirely different keys from the normal result (no scan_window,
// pending_dgws etc.; has a message the normal result never has).
type NoChipsRemainingResult struct {
	TeamID          int                `json:"team_id"`
	Gameweek        int                `json:"gameweek"`
	ChipsRemaining  []string           `json:"chips_remaining"`
	ChipsUsed       []ChipUsageDisplay `json:"chips_used"`
	Recommendations []any              `json:"recommendations"`
	Message         string             `json:"message"`
}

type ChipStrategyResult struct {
	Error string `json:"error,omitempty"`

	TeamID          int                  `json:"team_id"`
	Gameweek        int                  `json:"gameweek"`
	ScanWindow      string               `json:"scan_window"`
	ChipsRemaining  []string             `json:"chips_remaining"`
	ChipsUsed       []ChipUsageDisplay   `json:"chips_used"`
	Recommendations []any                `json:"recommendations"`
	PendingDGWs     *PendingDGWsSummary  `json:"pending_dgws,omitempty"`
	CommunityIntel  *ChipsCommunityIntel `json:"community_intel,omitempty"`
	// ChipPlaysByGW is keyed by gameweek number as a string, since JSON
	// object keys are always strings — the internal map has int keys and
	// json.dumps stringifies them the same way on encode.
	ChipPlaysByGW map[string]map[string]int `json:"chip_plays_by_gw,omitempty"`
}

type BBoostGWDetails struct {
	DGWTeams          int     `json:"dgw_teams"`
	PredictedDGWTeams int     `json:"predicted_dgw_teams"`
	FixtureCount      int     `json:"fixture_count"`
	AvgFDR            float64 `json:"avg_fdr"`
}

type BBoostRecommendation struct {
	Chip                string          `json:"chip"`
	ChipCode            string          `json:"chip_code"`
	RecommendedGameweek int             `json:"recommended_gameweek"`
	ConfidenceScore     float64         `json:"confidence_score"`
	Reasoning           string          `json:"reasoning"`
	GWDetails           BBoostGWDetails `json:"gw_details"`
}

type SuggestedCaptain struct {
	ID   int     `json:"id"`
	Name string  `json:"name"`
	Team string  `json:"team"`
	Form float64 `json:"form"`
}

type TCRecommendation struct {
	Chip                string            `json:"chip"`
	ChipCode            string            `json:"chip_code"`
	RecommendedGameweek int               `json:"recommended_gameweek"`
	ConfidenceScore     float64           `json:"confidence_score"`
	Reasoning           string            `json:"reasoning"`
	GWDetails           BBoostGWDetails   `json:"gw_details"`
	SuggestedCaptain    *SuggestedCaptain `json:"suggested_captain,omitempty"`
}

type FreeHitGWDetails struct {
	DGWTeams          int     `json:"dgw_teams"`
	PredictedDGWTeams int     `json:"predicted_dgw_teams"`
	BlankTeams        int     `json:"blank_teams"`
	FixtureCount      int     `json:"fixture_count"`
	AvgFDR            float64 `json:"avg_fdr"`
}

type FreeHitRecommendation struct {
	Chip                string           `json:"chip"`
	ChipCode            string           `json:"chip_code"`
	RecommendedGameweek int              `json:"recommended_gameweek"`
	ConfidenceScore     float64          `json:"confidence_score"`
	Reasoning           string           `json:"reasoning"`
	GWDetails           FreeHitGWDetails `json:"gw_details"`
}

type SquadIssues struct {
	PoorFormToughFixtures []string `json:"poor_form_tough_fixtures"`
	InjuredOrDoubtful     []string `json:"injured_or_doubtful"`
}

type WildcardRecommendation struct {
	Chip                string          `json:"chip"`
	ChipCode            string          `json:"chip_code"`
	RecommendedGameweek int             `json:"recommended_gameweek"`
	ConfidenceScore     float64         `json:"confidence_score"`
	Reasoning           string          `json:"reasoning"`
	SquadIssues         SquadIssues     `json:"squad_issues"`
	GWDetails           BBoostGWDetails `json:"gw_details"`
}

type PendingDGWsSummary struct {
	Summary string           `json:"summary"`
	Teams   []PendingDGWTeam `json:"teams"`
}

type PendingDGWTeam struct {
	Team                string   `json:"team"`
	TeamID              int      `json:"team_id"`
	UnscheduledFixtures []string `json:"unscheduled_fixtures"`
	LikelyDGWGameweeks  []int    `json:"likely_dgw_gameweeks"`
}

// ChipsCommunityIntel mirrors the subset of CommunityIntel chip strategy
// surfaces, only when at least one field is non-empty (see the
// `if intel_summary:` guard) — omitempty here is safe, unlike elsewhere in
// implementation, because callers already treat "empty" and "absent" the same
// way.
type ChipsCommunityIntel struct {
	PredictedDGWs map[string]SourcedMention `json:"predicted_dgws,omitempty"`
	PredictedBGWs map[string]SourcedMention `json:"predicted_bgws,omitempty"`
	Sources       []string                  `json:"sources,omitempty"`
	SourceErrors  []string                  `json:"source_errors,omitempty"`
}

func (c *ChipsCommunityIntel) empty() bool {
	return c == nil || (len(c.PredictedDGWs) == 0 && len(c.PredictedBGWs) == 0 && len(c.Sources) == 0 && len(c.SourceErrors) == 0)
}

// ChipStrategy recommends which of the remaining chips to play, and when.
func (e *Engine) ChipStrategy(ctx context.Context, teamID int) (any, error) {
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	fixtures, err := e.client.Fixtures(ctx)
	if err != nil {
		return nil, err
	}
	currentGW := bootstrap.CurrentGameweek()
	nextGW := bootstrap.NextGameweek()

	picks, err := e.client.TeamPicks(ctx, teamID, currentGW)
	if err != nil {
		return &ChipStrategyResult{
			Error: fmt.Sprintf("Could not fetch picks for team %d. Check the team ID is correct.", teamID),
		}, nil
	}
	history, err := e.client.TeamHistory(ctx, teamID)
	if err != nil {
		return nil, err
	}

	// Chips reset at the season's halfway point (after GW19), so "used"
	// means used within the current half, exactly mirroring
	// league_analyzer's chipsRemaining (same rule, independently
	// implemented independently — not shared code here either).
	const halfwayGW = 19
	usedNames := make(map[string]bool)
	for _, c := range history.Chips {
		inSecondHalf := c.Event > halfwayGW
		if (currentGW > halfwayGW) == inSecondHalf {
			usedNames[c.Name] = true
		}
	}
	chipsRemaining := make(map[string]bool)
	for _, c := range allChips {
		if !usedNames[c] {
			chipsRemaining[c] = true
		}
	}

	chipsUsedDisplay := make([]ChipUsageDisplay, 0, len(history.Chips))
	for _, c := range history.Chips {
		chipsUsedDisplay = append(chipsUsedDisplay, ChipUsageDisplay{Chip: displayName(c.Name), Gameweek: c.Event})
	}

	if len(chipsRemaining) == 0 {
		return &NoChipsRemainingResult{
			TeamID: teamID, Gameweek: currentGW,
			ChipsRemaining: []string{}, ChipsUsed: chipsUsedDisplay,
			Recommendations: []any{}, Message: "All chips have been used this season.",
		}, nil
	}

	playersByID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		playersByID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)
	allTeamIDs := make(map[int]bool, len(bootstrap.Teams))
	for i := range bootstrap.Teams {
		allTeamIDs[bootstrap.Teams[i].ID] = true
	}

	var squadPlayers, benchPlayers []*fpl.Player
	for _, pick := range picks.Picks {
		p := playersByID[pick.Element]
		if p == nil {
			continue
		}
		if pick.Position <= 11 {
			squadPlayers = append(squadPlayers, p)
		} else {
			benchPlayers = append(benchPlayers, p)
		}
	}

	scanGWs := make([]int, 0, chipScanWindow)
	for gw := nextGW; gw < nextGW+chipScanWindow && gw < 39; gw++ {
		scanGWs = append(scanGWs, gw)
	}

	teamsWithPending, pendingOrder := predictDGWTeams(fixtures)
	likelyDGWGWs := estimateLikelyDGWGameweeks(fixtures, scanGWs, teamsWithPending, pendingOrder)

	// Community intel is best-effort: a source being down or slow must never
	// break chip strategy, only narrow it back to API-only predictions.
	var communityIntel *CommunityIntel
	if ci, err := e.IntelFetcher.Fetch(ctx); err == nil {
		communityIntel = ci
		likelyDGWGWs = mergeLikelyDGWWithIntel(likelyDGWGWs, ci, teams)
	}

	predictedBlankTeams := make(map[int][]int) // gw -> team IDs
	if communityIntel != nil {
		shortToID := make(map[string]int, len(teams))
		for id, t := range teams {
			if t != nil {
				shortToID[t.ShortName] = id
			}
		}
		for gwStr, mention := range communityIntel.BGWs {
			gw, err := strconv.Atoi(gwStr)
			if err != nil {
				continue
			}
			var ids []int
			for _, short := range mention.Teams {
				if id, ok := shortToID[short]; ok {
					ids = append(ids, id)
				}
			}
			if len(ids) > 0 {
				predictedBlankTeams[gw] = ids
			}
		}
	}

	gwStats := make(map[int]*gwChipStats, len(scanGWs))
	for _, gw := range scanGWs {
		dgwTeams := countDGWTeams(fixtures, gw)
		blankTeams := countBlankingTeams(fixtures, gw, allTeamIDs)
		avgFDR := avgFDRForGW(fixtures, gw)
		fixCount := gwFixtureCount(fixtures, gw)
		fixtureMap := buildFixtureMap(fixtures, gw, teams)
		predictedDGW := likelyDGWGWs[gw]

		playing := make(map[int]bool)
		for i := range fixtures {
			if fixtures[i].InGameweek(gw) {
				playing[fixtures[i].TeamH] = true
				playing[fixtures[i].TeamA] = true
			}
		}
		extraBlanks := 0
		for _, tid := range predictedBlankTeams[gw] {
			if playing[tid] {
				extraBlanks++
			}
		}

		gwStats[gw] = &gwChipStats{
			dgwTeams: dgwTeams, predictedDGWTeams: predictedDGW,
			blankTeams: blankTeams + extraBlanks, avgFDR: avgFDR,
			fixtureCount: fixCount, fixtureMap: fixtureMap,
		}
	}

	assignment := findOptimalChipAssignment(chipsRemaining, scanGWs, gwStats, benchPlayers, squadPlayers, bootstrap.Elements, e)

	recommendations := buildChipRecommendations(assignment, gwStats, teams, squadPlayers)

	var pendingSummary *PendingDGWsSummary
	if len(teamsWithPending) > 0 {
		teamsOut := make([]PendingDGWTeam, 0, len(pendingOrder))
		for _, tid := range pendingOrder {
			var opponents []string
			for _, pf := range teamsWithPending[tid] {
				venue := "away"
				if pf.IsHome {
					venue = "home"
				}
				opponents = append(opponents, fmt.Sprintf("%s (%s)", shortName(teams[pf.Opponent]), venue))
			}
			var likelyGWs []int
			for _, gw := range scanGWs {
				if inIntSlice(likelyDGWGWs[gw], tid) {
					likelyGWs = append(likelyGWs, gw)
				}
			}
			if likelyGWs == nil {
				likelyGWs = []int{}
			}
			teamsOut = append(teamsOut, PendingDGWTeam{
				Team: shortName(teams[tid]), TeamID: tid,
				UnscheduledFixtures: opponents, LikelyDGWGameweeks: likelyGWs,
			})
		}
		pendingSummary = &PendingDGWsSummary{
			Summary: fmt.Sprintf(
				"%d teams have postponed fixtures that will create future DGWs. Consider waiting for official scheduling before using Triple Captain or Bench Boost.",
				len(teamsWithPending)),
			Teams: teamsOut,
		}
	}

	var intelSummary *ChipsCommunityIntel
	if communityIntel != nil {
		s := &ChipsCommunityIntel{
			Sources: communityIntel.SourcesChecked, SourceErrors: communityIntel.Errors,
		}
		if len(communityIntel.DGWs) > 0 {
			s.PredictedDGWs = communityIntel.DGWs
		}
		if len(communityIntel.BGWs) > 0 {
			s.PredictedBGWs = communityIntel.BGWs
		}
		if !s.empty() {
			intelSummary = s
		}
	}

	scanWindow := "N/A"
	if len(scanGWs) > 0 {
		scanWindow = fmt.Sprintf("GW%d-GW%d", scanGWs[0], scanGWs[len(scanGWs)-1])
	}

	chipsRemainingDisplay := make([]string, 0, len(chipsRemaining))
	for _, c := range allChips {
		if chipsRemaining[c] {
			chipsRemainingDisplay = append(chipsRemainingDisplay, displayName(c))
		}
	}

	result := &ChipStrategyResult{
		TeamID: teamID, Gameweek: currentGW, ScanWindow: scanWindow,
		ChipsRemaining: chipsRemainingDisplay, ChipsUsed: chipsUsedDisplay,
		Recommendations: recommendations, PendingDGWs: pendingSummary, CommunityIntel: intelSummary,
	}

	// Chip usage trends: both the scanned upcoming window and every finished
	// gameweek, so a caller can reason about community timing ("80k
	// managers used BB in GW34"). Two separate passes over events because
	// this is intentional — the second pass can revisit a gameweek
	// the first already touched (a finished GW inside the scan window,
	// possible right at a season's start/rollover), and the second write
	// simply overwrites the first with the same finished-event data.
	chipPlaysByGW := make(map[string]map[string]int)
	inScan := make(map[int]bool, len(scanGWs))
	for _, gw := range scanGWs {
		inScan[gw] = true
	}
	for i := range bootstrap.Events {
		ev := &bootstrap.Events[i]
		if ev.ID < nextGW || !inScan[ev.ID] {
			continue
		}
		if len(ev.ChipPlays) > 0 {
			chipPlaysByGW[strconv.Itoa(ev.ID)] = chipPlaysMap(ev.ChipPlays)
		}
	}
	for i := range bootstrap.Events {
		ev := &bootstrap.Events[i]
		if !ev.Finished {
			continue
		}
		if len(ev.ChipPlays) > 0 {
			chipPlaysByGW[strconv.Itoa(ev.ID)] = chipPlaysMap(ev.ChipPlays)
		}
	}
	if len(chipPlaysByGW) > 0 {
		result.ChipPlaysByGW = chipPlaysByGW
	}

	return result, nil
}

func chipPlaysMap(plays []fpl.ChipPlay) map[string]int {
	out := make(map[string]int, len(plays))
	for _, p := range plays {
		out[displayName(p.ChipName)] = p.NumPlayed
	}
	return out
}

// mergeLikelyDGWWithIntel folds community-predicted DGWs into the API-derived
// estimate, translating team short_names to IDs. A short_name that doesn't
// resolve to a known team is dropped rather than erroring.
func mergeLikelyDGWWithIntel(likely map[int][]int, intel *CommunityIntel, teams map[int]*fpl.Team) map[int][]int {
	if intel == nil {
		return likely
	}
	shortToID := make(map[string]int, len(teams))
	for id, t := range teams {
		if t != nil {
			shortToID[t.ShortName] = id
		}
	}

	out := make(map[int][]int, len(likely))
	for gw, ids := range likely {
		out[gw] = append([]int(nil), ids...)
	}
	for gwStr, mention := range intel.DGWs {
		gw, err := strconv.Atoi(gwStr)
		if err != nil {
			continue
		}
		existing := make(map[int]bool)
		for _, id := range out[gw] {
			existing[id] = true
		}
		for _, short := range mention.Teams {
			if id, ok := shortToID[short]; ok {
				existing[id] = true
			}
		}
		if len(existing) > 0 {
			ids := make([]int, 0, len(existing))
			for id := range existing {
				ids = append(ids, id)
			}
			sort.Ints(ids)
			out[gw] = ids
		}
	}
	return out
}

// buildChipRecommendations turns the optimizer's assignment into the four
// chip-specific recommendation shapes, then sorts by recommended gameweek —
// with Wildcard first as a tiebreaker, so a WC->BB combo reads in the order a
// manager would actually action it. Ties in (gameweek, chip) genuinely cannot
// occur: the optimizer's used-gameweek constraint guarantees every chip in
// the assignment lands on a distinct gameweek.
func buildChipRecommendations(assignment map[string]chipAssignment, gwStats map[int]*gwChipStats, teams map[int]*fpl.Team, squadPlayers []*fpl.Player) []any {
	recs := make([]any, 0, len(assignment))

	if a, ok := assignment["bboost"]; ok {
		recs = append(recs, buildBBoostRecommendation(a, gwStats[a.gw], assignment))
	}
	if a, ok := assignment["3xc"]; ok {
		recs = append(recs, buildTCRecommendation(a, gwStats[a.gw], teams))
	}
	if a, ok := assignment["freehit"]; ok {
		recs = append(recs, buildFreeHitRecommendation(a, gwStats[a.gw]))
	}
	if a, ok := assignment["wildcard"]; ok {
		recs = append(recs, buildWildcardRecommendation(a, gwStats[a.gw], squadPlayers, assignment, gwStats))
	}

	chipOrder := map[string]int{"wildcard": 0, "bboost": 1, "freehit": 2, "3xc": 3}
	codeOf := func(v any) (string, int) {
		switch r := v.(type) {
		case *BBoostRecommendation:
			return r.ChipCode, r.RecommendedGameweek
		case *TCRecommendation:
			return r.ChipCode, r.RecommendedGameweek
		case *FreeHitRecommendation:
			return r.ChipCode, r.RecommendedGameweek
		case *WildcardRecommendation:
			return r.ChipCode, r.RecommendedGameweek
		}
		return "", 0
	}
	sort.SliceStable(recs, func(i, j int) bool {
		ci, gwi := codeOf(recs[i])
		cj, gwj := codeOf(recs[j])
		if gwi != gwj {
			return gwi < gwj
		}
		return chipOrder[ci] < chipOrder[cj]
	})

	return recs
}

func buildBBoostRecommendation(a chipAssignment, stats *gwChipStats, assignment map[string]chipAssignment) *BBoostRecommendation {
	var parts []string
	if stats.dgwTeams > 0 {
		parts = append(parts, fmt.Sprintf("%d teams have confirmed DGW", stats.dgwTeams))
	}
	if len(stats.predictedDGWTeams) > 0 {
		parts = append(parts, fmt.Sprintf("%d teams have potential DGW (postponed fixtures pending rescheduling)", len(stats.predictedDGWTeams)))
	}
	if stats.fixtureCount > 10 {
		parts = append(parts, fmt.Sprintf("%d fixtures scheduled", stats.fixtureCount))
	}
	parts = append(parts, "avg FDR "+FloatStr(stats.avgFDR))
	if wc, ok := assignment["wildcard"]; ok && wc.gw == a.gw-1 {
		parts = append(parts, fmt.Sprintf("use Wildcard in GW%d to rebuild bench specifically for this DGW", wc.gw))
	}

	return &BBoostRecommendation{
		Chip: "Bench Boost", ChipCode: "bboost", RecommendedGameweek: a.gw,
		ConfidenceScore: Round(a.score, 1), Reasoning: strings.Join(parts, ". ") + ".",
		GWDetails: BBoostGWDetails{
			DGWTeams: stats.dgwTeams, PredictedDGWTeams: len(stats.predictedDGWTeams),
			FixtureCount: stats.fixtureCount, AvgFDR: stats.avgFDR,
		},
	}
}

func buildTCRecommendation(a chipAssignment, stats *gwChipStats, teams map[int]*fpl.Team) *TCRecommendation {
	var parts []string
	if a.tcPlayer != nil {
		team := shortName(teams[a.tcPlayer.Team])
		parts = append(parts, fmt.Sprintf("Best captain option is %s (%s)", a.tcPlayer.WebName, team))
		fixes := stats.fixtureMap[a.tcPlayer.Team]
		switch {
		case len(fixes) > 1:
			parts = append(parts, fmt.Sprintf("%s has %d confirmed fixtures (DGW)", a.tcPlayer.WebName, len(fixes)))
		case inIntSlice(stats.predictedDGWTeams, a.tcPlayer.Team):
			parts = append(parts, fmt.Sprintf("%s's team has a postponed fixture pending", a.tcPlayer.WebName))
		}
	}
	if stats.dgwTeams > 0 {
		parts = append(parts, fmt.Sprintf("%d teams have confirmed DGW", stats.dgwTeams))
	}
	if len(stats.predictedDGWTeams) > 0 {
		parts = append(parts, fmt.Sprintf("%d teams likely to have DGW", len(stats.predictedDGWTeams)))
	}

	rec := &TCRecommendation{
		Chip: "Triple Captain", ChipCode: "3xc", RecommendedGameweek: a.gw,
		ConfidenceScore: Round(a.score, 1), Reasoning: strings.Join(parts, ". ") + ".",
		GWDetails: BBoostGWDetails{
			DGWTeams: stats.dgwTeams, PredictedDGWTeams: len(stats.predictedDGWTeams),
			FixtureCount: stats.fixtureCount, AvgFDR: stats.avgFDR,
		},
	}
	if a.tcPlayer != nil {
		rec.SuggestedCaptain = &SuggestedCaptain{
			ID: a.tcPlayer.ID, Name: a.tcPlayer.WebName, Team: shortName(teams[a.tcPlayer.Team]),
			Form: a.tcPlayer.Form.Float(),
		}
	}
	return rec
}

func buildFreeHitRecommendation(a chipAssignment, stats *gwChipStats) *FreeHitRecommendation {
	var parts []string
	if stats.blankTeams > 0 {
		parts = append(parts, fmt.Sprintf("%d teams have no fixture (blank GW)", stats.blankTeams))
	}
	if stats.blankTeams >= 10 {
		parts = append(parts, "major BGW — Free Hit is essential to field 11 playing players")
	}
	if stats.dgwTeams > 0 {
		parts = append(parts, fmt.Sprintf("%d teams have confirmed DGW", stats.dgwTeams))
	}
	if len(stats.predictedDGWTeams) > 0 {
		parts = append(parts, fmt.Sprintf("%d teams likely to have DGW", len(stats.predictedDGWTeams)))
	}
	if len(parts) == 0 {
		parts = append(parts, "best fixture variance for squad optimization")
	}

	return &FreeHitRecommendation{
		Chip: "Free Hit", ChipCode: "freehit", RecommendedGameweek: a.gw,
		ConfidenceScore: Round(a.score, 1), Reasoning: strings.Join(parts, ". ") + ".",
		GWDetails: FreeHitGWDetails{
			DGWTeams: stats.dgwTeams, PredictedDGWTeams: len(stats.predictedDGWTeams),
			BlankTeams: stats.blankTeams, FixtureCount: stats.fixtureCount, AvgFDR: stats.avgFDR,
		},
	}
}

func buildWildcardRecommendation(a chipAssignment, stats *gwChipStats, squadPlayers []*fpl.Player, assignment map[string]chipAssignment, gwStats map[int]*gwChipStats) *WildcardRecommendation {
	var troubled, injured []string
	for _, p := range squadPlayers {
		form := p.Form.Float()
		avgFDR := 3.0
		if fixes := stats.fixtureMap[p.Team]; len(fixes) > 0 {
			sum := 0.0
			for _, f := range fixes {
				sum += f.FDR
			}
			avgFDR = sum / float64(len(fixes))
		}
		if form <= 3.0 && avgFDR >= 3.5 {
			troubled = append(troubled, p.WebName)
		}
		if InjuryStatuses[p.Status] {
			injured = append(injured, p.WebName)
		}
	}

	var parts []string
	if bb, ok := assignment["bboost"]; ok && a.gw == bb.gw-1 {
		bbStats := gwStats[bb.gw]
		parts = append(parts, fmt.Sprintf(
			"Wildcard→Bench Boost combo: rebuild squad and bench for GW%d mega DGW (%d confirmed + %d predicted DGW teams)",
			bb.gw, bbStats.dgwTeams, len(bbStats.predictedDGWTeams)))
	}
	if len(troubled) > 0 {
		shown := troubled
		if len(shown) > 4 {
			shown = shown[:4]
		}
		parts = append(parts, fmt.Sprintf("%d squad players have poor form + tough fixtures (%s)", len(troubled), strings.Join(shown, ", ")))
	}
	if len(injured) > 0 {
		shown := injured
		if len(shown) > 3 {
			shown = shown[:3]
		}
		parts = append(parts, fmt.Sprintf("%d injured/doubtful (%s)", len(injured), strings.Join(shown, ", ")))
	}
	if len(parts) == 0 {
		parts = append(parts, "good fixture swings available for replacements")
	}

	if troubled == nil {
		troubled = []string{}
	}
	if injured == nil {
		injured = []string{}
	}

	return &WildcardRecommendation{
		Chip: "Wildcard", ChipCode: "wildcard", RecommendedGameweek: a.gw,
		ConfidenceScore: Round(a.score, 1), Reasoning: strings.Join(parts, ". ") + ".",
		SquadIssues: SquadIssues{PoorFormToughFixtures: troubled, InjuredOrDoubtful: injured},
		GWDetails: BBoostGWDetails{
			DGWTeams: stats.dgwTeams, PredictedDGWTeams: len(stats.predictedDGWTeams),
			FixtureCount: stats.fixtureCount, AvgFDR: stats.avgFDR,
		},
	}
}

// bestGWByScore returns the gameweek with the highest score, breaking ties by
// earliest gameweek — equal values retain the first encountered gameweek,
// insertion order, and scanGWs (ascending) is exactly that insertion order
// here since every score map is built by iterating scanGWs.
func bestGWByScore(scores map[int]float64, scanGWs []int) *int {
	if len(scores) == 0 {
		return nil
	}
	var best *int
	bestScore := 0.0
	for _, gw := range scanGWs {
		s, ok := scores[gw]
		if !ok {
			continue
		}
		if best == nil || s > bestScore {
			g := gw
			best = &g
			bestScore = s
		}
	}
	return best
}
