package algo

import (
	"context"
	"fmt"
	"slices"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Chip confidence, alternatives and factors for chip_strategy (issue #54).
//
// Each chip's score comes from its own formula (scoreBBForGW, scoreTCForGW,
// scoreFHForGW, scoreWCForGW), in units that mean nothing outside that chip:
// a Bench Boost earns 8 per confirmed double-gameweek team, a Wildcard a flat
// 50 for setting up a Bench Boost, a Triple Captain a captain score
// multiplied for doubles. The old confidence_score was that raw number, so a
// Wildcard's 9.6 and a Triple Captain's 13 could not be compared.
//
// confidence is now scale-free: how far the recommended gameweek beats the
// same chip's next-best gameweek, as a percentage of its score. Both scores
// are in the same chip's units, so the ratio is comparable across chips. It
// is a measure of how clear-cut the choice is, not a probability that the
// chip will pay off: there is no record of chip outcomes to calibrate one
// against.

// Confidence labels by margin over the next-best gameweek. Judgement calls:
// at a quarter the choice is clear; under a tenth, a fixture change or one
// injury could flip it.
const (
	chipMarginHigh   = 25.0
	chipMarginMedium = 10.0
	chipCandidates   = 5
)

const chipConfidenceScale = "chip_score is in each chip's own units (Bench Boost, Triple Captain, Free Hit and Wildcard are scored by different formulas), so compare it only between gameweeks for the same chip, never across chips. confidence is comparable across chips: it is the recommended gameweek's margin over the same chip's next-best gameweek, as a share of its score (margin_over_next_pct), labelled high at 25% or more, medium at 10% or more, and low below that. It says how clear-cut the choice of gameweek is, not how likely the chip is to pay off. confidence_score is deprecated and equals chip_score."

// ChipCandidateFactors are the inputs behind one gameweek's chip score.
type ChipCandidateFactors struct {
	FixtureCount      int     `json:"fixture_count"`
	DGWTeams          int     `json:"dgw_teams"`           // confirmed doubles
	PredictedDGWTeams int     `json:"predicted_dgw_teams"` // likely doubles from postponements and community intel
	BlankTeams        int     `json:"blank_teams"`
	AvgFDR            float64 `json:"avg_fdr"`
	// Chip-specific squad fit.
	BenchPlayersPlaying *int   `json:"bench_players_playing,omitempty"` // Bench Boost
	BenchFixtures       *int   `json:"bench_fixtures,omitempty"`        // Bench Boost
	Captain             string `json:"captain,omitempty"`               // Triple Captain
}

// ChipCandidate is one gameweek a chip could be played in.
type ChipCandidate struct {
	Gameweek  int                  `json:"gameweek"`
	ChipScore float64              `json:"chip_score"`
	Factors   ChipCandidateFactors `json:"factors"`
}

// ChipConfidence is embedded in every chip recommendation.
type ChipConfidence struct {
	ChipScore        float64         `json:"chip_score"`
	NextBestGameweek int             `json:"next_best_gameweek,omitempty"`
	NextBestScore    float64         `json:"next_best_score"`
	GapToNext        float64         `json:"gap_to_next"`
	MarginPct        float64         `json:"margin_over_next_pct"`
	Confidence       string          `json:"confidence"` // high, medium or low; see chipConfidenceScale
	Candidates       []ChipCandidate `json:"candidates"` // best first, the recommended gameweek included
	Note             string          `json:"confidence_note,omitempty"`
}

// addChipConfidence fills in every recommendation's ChipConfidence from the
// optimiser's per-chip score tables.
func addChipConfidence(recs []any, tables map[string]map[int]chipAssignment, scanGWs []int, gwStats map[int]*gwChipStats, bench []*fpl.Player) {
	for _, r := range recs {
		var code string
		var gw int
		var c *ChipConfidence
		switch v := r.(type) {
		case *BBoostRecommendation:
			code, gw, c = v.ChipCode, v.RecommendedGameweek, &v.ChipConfidence
		case *TCRecommendation:
			code, gw, c = v.ChipCode, v.RecommendedGameweek, &v.ChipConfidence
		case *FreeHitRecommendation:
			code, gw, c = v.ChipCode, v.RecommendedGameweek, &v.ChipConfidence
		case *WildcardRecommendation:
			code, gw, c = v.ChipCode, v.RecommendedGameweek, &v.ChipConfidence
		default:
			continue
		}
		*c = chipConfidence(code, gw, tables[code], scanGWs, gwStats, bench)
	}
}

// chipConfidence compares the recommended gameweek with the chip's others.
func chipConfidence(code string, gw int, table map[int]chipAssignment, scanGWs []int, gwStats map[int]*gwChipStats, bench []*fpl.Player) ChipConfidence {
	// Gameweeks best first; equal scores keep calendar order.
	gws := make([]int, 0, len(scanGWs))
	for _, g := range scanGWs {
		if _, ok := table[g]; ok {
			gws = append(gws, g)
		}
	}
	slices.SortStableFunc(gws, func(a, b int) int {
		switch {
		case table[a].score > table[b].score:
			return -1
		case table[a].score < table[b].score:
			return 1
		default:
			return 0
		}
	})

	out := ChipConfidence{ChipScore: Round(table[gw].score, 1), Candidates: []ChipCandidate{}}
	for _, g := range gws {
		if len(out.Candidates) == chipCandidates {
			break
		}
		out.Candidates = append(out.Candidates, ChipCandidate{
			Gameweek: g, ChipScore: Round(table[g].score, 1), Factors: chipFactors(code, gwStats[g], table[g], bench),
		})
	}

	// The next best is the chip's best other gameweek.
	for _, g := range gws {
		if g != gw {
			out.NextBestGameweek, out.NextBestScore = g, Round(table[g].score, 1)
			break
		}
	}
	if out.NextBestGameweek == 0 {
		out.Confidence = "low"
		out.Note = "Only one gameweek was scanned, so there is nothing to compare it with."
		return out
	}
	out.GapToNext = Round(out.ChipScore-out.NextBestScore, 1)
	if out.ChipScore > 0 {
		out.MarginPct = Round(out.GapToNext/out.ChipScore*100, 1)
	}

	switch {
	case out.GapToNext < 0:
		// The joint plan gave this chip's own best gameweek to another chip.
		out.Confidence = "low"
		out.Note = fmt.Sprintf("This chip's own best gameweek is GW%d (%.1f). The plan uses GW%d because another chip scores more in GW%d, and two chips can't share a gameweek.",
			out.NextBestGameweek, out.NextBestScore, gw, out.NextBestGameweek)
	case out.MarginPct >= chipMarginHigh:
		out.Confidence = "high"
	case out.MarginPct >= chipMarginMedium:
		out.Confidence = "medium"
	default:
		out.Confidence = "low"
	}
	return out
}

// chipFactors are one gameweek's inputs, plus the chip's squad fit.
func chipFactors(code string, stats *gwChipStats, a chipAssignment, bench []*fpl.Player) ChipCandidateFactors {
	if stats == nil {
		return ChipCandidateFactors{}
	}
	f := ChipCandidateFactors{
		FixtureCount: stats.fixtureCount, DGWTeams: stats.dgwTeams,
		PredictedDGWTeams: len(stats.predictedDGWTeams), BlankTeams: stats.blankTeams,
		AvgFDR: Round(stats.avgFDR, 2),
	}
	switch code {
	case "bboost":
		playing, fixtures := 0, 0
		for _, p := range bench {
			if n := len(stats.fixtureMap[p.Team]); n > 0 {
				playing++
				fixtures += n
			}
		}
		f.BenchPlayersPlaying, f.BenchFixtures = ptr(playing), ptr(fixtures)
	case "3xc":
		if a.tcPlayer != nil {
			f.Captain = a.tcPlayer.WebName
		}
	}
	return f
}

// SeasonOutlookGW is a gameweek after the scan window with confirmed
// doubles or blanks.
type SeasonOutlookGW struct {
	Gameweek   int `json:"gameweek"`
	DGWTeams   int `json:"dgw_teams"`
	BlankTeams int `json:"blank_teams"`
}

// seasonOutlook lists every gameweek from fromGW to 38 where the fixture list
// already has a double or a blank. Later postponements will add more; this
// shows only what FPL has scheduled.
func seasonOutlook(fixtures []fpl.Fixture, fromGW int, allTeamIDs map[int]bool) []SeasonOutlookGW {
	out := []SeasonOutlookGW{}
	for gw := max(fromGW, 1); gw <= 38; gw++ {
		dgw := countDGWTeams(fixtures, gw)
		blank := countBlankingTeams(fixtures, gw, allTeamIDs)
		if dgw > 0 || blank > 0 {
			out = append(out, SeasonOutlookGW{Gameweek: gw, DGWTeams: dgw, BlankTeams: blank})
		}
	}
	return out
}

// LeagueChipsRow is one rival's chips left in this half of the season.
type LeagueChipsRow struct {
	Rank           int      `json:"rank"`
	ManagerName    string   `json:"manager_name"`
	TeamID         int      `json:"team_id"`
	ChipsRemaining []string `json:"chips_remaining"`
}

// maxLeagueChipRivals caps the history requests a league_id adds.
const maxLeagueChipRivals = 20

// ChipStrategyWith is ChipStrategy with an optional league: given one, the
// result adds each rival's chips left this half, for the top 20 of the
// standings page.
func (e *Engine) ChipStrategyWith(ctx context.Context, teamID int, leagueID *int) (any, error) {
	out, err := e.ChipStrategy(ctx, teamID)
	if err != nil || leagueID == nil {
		return out, err
	}
	res, ok := out.(*ChipStrategyResult)
	if !ok {
		return out, nil
	}
	standings, err := e.client.LeagueStandings(ctx, *leagueID)
	if err != nil {
		res.LeagueChipsNote = fmt.Sprintf("Could not fetch league %d, so rivals' chips are not shown.", *leagueID)
		return res, nil
	}

	rows := []LeagueChipsRow{}
	missing := 0
	for _, s := range standings.Standings.Results {
		if s.Entry == teamID {
			continue
		}
		if len(rows)+missing == maxLeagueChipRivals {
			break
		}
		h, err := e.client.TeamHistory(ctx, s.Entry)
		if err != nil {
			missing++
			continue
		}
		var remaining []string
		for _, c := range chipsRemaining(h.Chips, res.Gameweek) {
			remaining = append(remaining, displayName(c))
		}
		rows = append(rows, LeagueChipsRow{Rank: s.Rank, ManagerName: s.PlayerName, TeamID: s.Entry, ChipsRemaining: remaining})
	}
	res.LeagueChips = rows
	if missing > 0 {
		res.LeagueChipsNote = fmt.Sprintf("%d rivals' histories could not be fetched and are left out.", missing)
	}
	return res, nil
}
