package algo

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/insights"
)

// Fixture Outlook — ranks teams by aggregate fixture difficulty over the next
// N gameweeks.

// homeWeight discounts home fixtures: home advantage eases difficulty, so a
// home FDR counts 85%.
const homeWeight = 0.85

type OutlookFixture struct {
	Gameweek    int     `json:"gameweek"`
	Opponent    string  `json:"opponent"`
	Venue       string  `json:"venue"`
	FDR         float64 `json:"fdr"`
	WeightedFDR float64 `json:"weighted_fdr"`
	// Congested is the cross-competition fixture-congestion signal (see
	// Engine.CongestionSource's doc): true when this team played a match
	// within insights.ShortRestThresholdDays days beforehand, across every
	// competition. Present only when CongestionSource is configured and the
	// season has cross-competition coverage; informational, not folded into
	// FDR/WeightedFDR/AdjustedDifficulty.
	Congested bool `json:"congested,omitempty"`
}

type TeamOutlook struct {
	TeamID             int              `json:"team_id"`
	Team               string           `json:"team"`
	TeamName           string           `json:"team_name"`
	AvgDifficulty      float64          `json:"avg_difficulty"`
	FixtureVariance    float64          `json:"fixture_variance"`
	AdjustedDifficulty float64          `json:"adjusted_difficulty"`
	FixtureCount       int              `json:"fixture_count"`
	Fixtures           []OutlookFixture `json:"fixtures"`
	Rank               int              `json:"rank"`
}

type TargetPlayer struct {
	Name          string  `json:"name"`
	Team          string  `json:"team"`
	TeamFullName  string  `json:"team_full_name"`
	Position      string  `json:"position"`
	Cost          float64 `json:"cost"`
	Form          float64 `json:"form"`
	PointsPerGame float64 `json:"points_per_game"`
	SelectedByPct float64 `json:"selected_by_pct"`
}

type FixtureOutlookResult struct {
	CurrentGameweek    int            `json:"current_gameweek"`
	GameweeksAhead     int            `json:"gameweeks_ahead"`
	TargetGameweeks    []int          `json:"target_gameweeks"`
	PositionFilter     *string        `json:"position_filter"`
	TeamsByDifficulty  []TeamOutlook  `json:"teams_by_difficulty"`
	NumPlayersToTarget int            `json:"num_players_to_target"`
	PlayersToTarget    []TargetPlayer `json:"players_to_target"`
}

var positionNames = map[string]bool{"GKP": true, "DEF": true, "MID": true, "FWD": true}

// FixtureOutlook summarizes each team's fixture difficulty over the next
// N gameweeks.
//
// gameweeksAhead defaults to 5 when non-positive; position is optional and
// filters the suggested players.
func (e *Engine) FixtureOutlook(ctx context.Context, gameweeksAhead int, position string) (*FixtureOutlookResult, error) {
	if gameweeksAhead <= 0 {
		gameweeksAhead = 5
	}

	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	fixtures, err := e.client.Fixtures(ctx)
	if err != nil {
		return nil, err
	}

	currentGW := bootstrap.CurrentGameweek()
	targetGWs := make([]int, 0, gameweeksAhead)
	inTarget := make(map[int]bool, gameweeksAhead)
	for gw := currentGW; gw < currentGW+gameweeksAhead; gw++ {
		targetGWs = append(targetGWs, gw)
		inTarget[gw] = true
	}

	teams := teamsByID(bootstrap)
	teamFixtures := make(map[int][]OutlookFixture, len(bootstrap.Teams))
	calendar := e.congestionCalendar(ctx, currentGW, currentGW+gameweeksAhead-1)
	codeByTeamID := make(map[int]int, len(bootstrap.Teams))
	for i := range bootstrap.Teams {
		codeByTeamID[bootstrap.Teams[i].ID] = bootstrap.Teams[i].Code
	}

	for i := range fixtures {
		f := &fixtures[i]
		gw, ok := f.EventOf()
		if !ok || !inTarget[gw] {
			continue
		}
		kt, ktErr := time.Parse(time.RFC3339, f.KickoffTime)

		// Note this differs from captain.go: the outlook blends against the
		// opponent's *attack* strength, where captaincy blends against their
		// defence. Both are required by the fixture-difficulty contract.
		homeFDR := blendFDR(float64(f.TeamHDifficulty),
			strengthOr(teams[f.TeamA], func(t *fpl.Team) int { return t.StrengthAttackAway }))
		awayFDR := blendFDR(float64(f.TeamADifficulty),
			strengthOr(teams[f.TeamH], func(t *fpl.Team) int { return t.StrengthAttackHome }))

		homeCongested, awayCongested := false, false
		if ktErr == nil {
			homeCongested = isCongested(calendar, codeByTeamID[f.TeamH], kt)
			awayCongested = isCongested(calendar, codeByTeamID[f.TeamA], kt)
		}

		teamFixtures[f.TeamH] = append(teamFixtures[f.TeamH], OutlookFixture{
			Gameweek:    gw,
			Opponent:    shortName(teams[f.TeamA]),
			Venue:       "H",
			FDR:         homeFDR,
			WeightedFDR: homeFDR * homeWeight,
			Congested:   homeCongested,
		})
		teamFixtures[f.TeamA] = append(teamFixtures[f.TeamA], OutlookFixture{
			Gameweek:    gw,
			Opponent:    shortName(teams[f.TeamH]),
			Venue:       "A",
			FDR:         awayFDR,
			WeightedFDR: awayFDR,
			Congested:   awayCongested,
		})
	}

	// Iterate the teams slice, not a map: the downstream sort is stable, so map
	// iteration would make the ranking of equal-difficulty teams nondeterministic.
	scores := make([]TeamOutlook, 0, len(bootstrap.Teams))
	for i := range bootstrap.Teams {
		team := &bootstrap.Teams[i]
		tf := teamFixtures[team.ID]

		avg, variance := 3.0, 0.0
		count := 0
		if len(tf) > 0 {
			count = len(tf)
			sum := 0.0
			for _, f := range tf {
				sum += f.WeightedFDR
			}
			mean := sum / float64(len(tf))
			avg = Round(mean, 2)
			if len(tf) > 1 {
				// Population variance, against the unrounded mean.
				ss := 0.0
				for _, f := range tf {
					d := f.WeightedFDR - mean
					ss += d * d
				}
				variance = Round(ss/float64(len(tf)), 2)
			}
		}

		sorted := slices.Clone(tf)
		slices.SortStableFunc(sorted, func(a, b OutlookFixture) int {
			return a.Gameweek - b.Gameweek
		})
		if sorted == nil {
			sorted = []OutlookFixture{}
		}

		scores = append(scores, TeamOutlook{
			TeamID:   team.ID,
			Team:     team.ShortName,
			TeamName: team.Name,
			// A high-variance run is riskier than a steady one at the same mean.
			AvgDifficulty:      avg,
			FixtureVariance:    variance,
			AdjustedDifficulty: Round(avg+variance*0.15, 2),
			FixtureCount:       count,
			Fixtures:           sorted,
		})
	}

	slices.SortStableFunc(scores, func(a, b TeamOutlook) int {
		switch {
		case a.AdjustedDifficulty < b.AdjustedDifficulty:
			return -1
		case a.AdjustedDifficulty > b.AdjustedDifficulty:
			return 1
		default:
			return 0
		}
	})
	for i := range scores {
		scores[i].Rank = i + 1
	}

	var positionFilter map[int]bool
	var positionOut *string
	if position != "" {
		upper := strings.ToUpper(position)
		positionOut = &upper
		if positionNames[upper] {
			positionFilter = map[int]bool{}
			for k, v := range PositionMap {
				if v == upper {
					positionFilter[k] = true
				}
			}
		}
	}

	easiest := make(map[int]bool, 5)
	for i := 0; i < len(scores) && i < 5; i++ {
		easiest[scores[i].TeamID] = true
	}

	// Teams with a fixture in the immediate gameweek; anyone else is blanking.
	withNextFixture := make(map[int]bool)
	for i := range fixtures {
		if fixtures[i].InGameweek(currentGW) {
			withNextFixture[fixtures[i].TeamH] = true
			withNextFixture[fixtures[i].TeamA] = true
		}
	}

	candidates := make([]*fpl.Player, 0, 64)
	for i := range bootstrap.Elements {
		p := &bootstrap.Elements[i]
		if !easiest[p.Team] || !withNextFixture[p.Team] {
			continue
		}
		if positionFilter != nil && !positionFilter[p.ElementType] {
			continue
		}
		if InjuryStatuses[p.Status] {
			continue
		}
		candidates = append(candidates, p)
	}

	// Stability is load-bearing here. In preseason FPL resets form to 0.0 for
	// every player, so this sort is entirely ties and the output order is
	// decided by stability alone.
	slices.SortStableFunc(candidates, func(a, b *fpl.Player) int {
		switch {
		case a.Form.Float() > b.Form.Float():
			return -1
		case a.Form.Float() < b.Form.Float():
			return 1
		default:
			return 0
		}
	})

	targets := make([]TargetPlayer, 0, 10)
	for i := 0; i < len(candidates) && i < 10; i++ {
		p := candidates[i]
		targets = append(targets, TargetPlayer{
			Name:          p.WebName,
			Team:          shortName(teams[p.Team]),
			TeamFullName:  fullName(teams[p.Team]),
			Position:      Position(p.ElementType),
			Cost:          float64(p.NowCost) / 10,
			Form:          p.Form.Float(),
			PointsPerGame: p.PointsPerGame.Float(),
			SelectedByPct: p.SelectedByPercent.Float(),
		})
	}

	return &FixtureOutlookResult{
		CurrentGameweek:    currentGW,
		GameweeksAhead:     gameweeksAhead,
		TargetGameweeks:    targetGWs,
		PositionFilter:     positionOut,
		TeamsByDifficulty:  scores,
		NumPlayersToTarget: len(targets),
		PlayersToTarget:    targets,
	}, nil
}

// congestionCalendar best-effort fetches each team's cross-competition
// fixture calendar covering the outlook window, plus one gameweek of
// context before it so the first target gameweek's rest-days check has a
// prior fixture to compare against. Returns nil (no signal for anyone) if
// CongestionSource isn't configured or the fetch fails for any reason —
// this is enrichment, never allowed to break FixtureOutlook, matching
// finishingLuckMap's contract in differentials.go.
func (e *Engine) congestionCalendar(ctx context.Context, fromGW, toGW int) map[int][]time.Time {
	if e.CongestionSource == nil {
		return nil
	}
	probeFrom := fromGW - 1
	if probeFrom < 1 {
		probeFrom = 1
	}
	calendar, err := e.CongestionSource.TeamFixtureCalendar(ctx, currentInsightsSeason(e.Now()), probeFrom, toGW)
	if err != nil {
		return nil
	}
	return calendar
}

// isCongested reports whether team code's calendar shows a fixture within
// insights.ShortRestThresholdDays days before ref. A missing calendar or
// unknown team code (code == 0, e.g. a club without an FPL-Core-Insights
// mapping) reports false rather than erroring.
func isCongested(calendar map[int][]time.Time, code int, ref time.Time) bool {
	if calendar == nil || code == 0 {
		return false
	}
	rest, ok := insights.RestDaysBefore(calendar[code], ref)
	return ok && rest <= insights.ShortRestThresholdDays
}
