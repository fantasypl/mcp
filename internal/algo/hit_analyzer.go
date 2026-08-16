package algo

import (
	"context"
	"fmt"
	"slices"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Hit analysis answers a single question: is this transfer worth −4 points?
//
// Both players are projected over the next N gameweeks from a blend of recent
// form and season points-per-game, adjusted per fixture for difficulty and
// venue. If the projected gain clears 4 points, the hit pays for itself.
//
// Note this uses FPL's raw 1-5 difficulty rather than the strength-blended
// figure the captain model uses. The multiplier table below is calibrated
// against those integer ratings.

const (
	homeBoost   = 1.15 // home fixtures are worth ~15% more
	awayPenalty = 0.95 // away fixtures ~5% less
	hitCost     = -4
)

// fdrMultiplier scales expected points by fixture difficulty.
var fdrMultiplier = map[int]float64{
	1: 1.30, // very easy
	2: 1.15, // easy
	3: 1.00, // average
	4: 0.85, // tough
	5: 0.70, // very tough
}

// projectionFixture is one upcoming fixture in the projection window.
type projectionFixture struct {
	Gameweek int
	FDR      int
	IsHome   bool
	Opponent int
}

type HitResult struct {
	Error string `json:"error,omitempty"`

	Gameweek           int            `json:"gameweek,omitempty"`
	GameweeksProjected int            `json:"gameweeks_projected,omitempty"`
	PlayerOut          *PlayerSummary `json:"player_out,omitempty"`
	PlayerIn           *PlayerSummary `json:"player_in,omitempty"`
	Analysis           *HitAnalysis   `json:"analysis,omitempty"`
	Verdict            string         `json:"verdict,omitempty"`
}

type PlayerSummary struct {
	ID                      int             `json:"id"`
	Name                    string          `json:"name"`
	Team                    string          `json:"team"`
	Position                string          `json:"position"`
	Cost                    float64         `json:"cost"`
	Form                    float64         `json:"form"`
	PointsPerGame           float64         `json:"points_per_game"`
	TotalPoints             int             `json:"total_points"`
	Status                  string          `json:"status"`
	ExpectedPointsProjected float64         `json:"expected_points_projected"`
	Fixtures                []FixtureDetail `json:"fixtures"`
}

type FixtureDetail struct {
	Gameweek int    `json:"gameweek"`
	Opponent string `json:"opponent"`
	FDR      int    `json:"fdr"`
}

type HitAnalysis struct {
	PlayerOutExpectedPoints float64 `json:"player_out_expected_points"`
	PlayerInExpectedPoints  float64 `json:"player_in_expected_points"`
	ProjectedGain           float64 `json:"projected_gain"`
	HitCost                 int     `json:"hit_cost"`
	NetAfterHit             float64 `json:"net_after_hit"`
	HitWorthIt              bool    `json:"hit_worth_it"`
	Confidence              string  `json:"confidence"`
}

// buildProjectionWindow maps each team to its fixtures across the projection
// window, starting at startGW.
func buildProjectionWindow(fixtures []fpl.Fixture, startGW, numGWs int) map[int][]projectionFixture {
	target := make(map[int]bool, numGWs)
	for gw := startGW; gw < startGW+numGWs; gw++ {
		target[gw] = true
	}

	out := make(map[int][]projectionFixture)
	for i := range fixtures {
		f := &fixtures[i]
		gw, ok := f.EventOf()
		if !ok || !target[gw] {
			continue
		}
		out[f.TeamH] = append(out[f.TeamH], projectionFixture{
			Gameweek: gw, FDR: f.TeamHDifficulty, IsHome: true, Opponent: f.TeamA,
		})
		out[f.TeamA] = append(out[f.TeamA], projectionFixture{
			Gameweek: gw, FDR: f.TeamADifficulty, IsHome: false, Opponent: f.TeamH,
		})
	}
	return out
}

// projectExpectedPoints estimates a player's return across the window.
//
// Availability is applied as a straight multiplier: a flagged player with a
// stated chance scales by it, and a flagged player with no stated chance is
// assumed out entirely rather than optimistically included.
func projectExpectedPoints(p *fpl.Player, fixtures []projectionFixture) float64 {
	// Form is weighted above season average because it is more recent.
	baseRate := p.Form.Float()*0.6 + p.PointsPerGame.Float()*0.4

	playingPct := 1.0
	if InjuryStatuses[p.Status] {
		if p.ChanceOfPlayingNextRound != nil {
			playingPct = float64(*p.ChanceOfPlayingNextRound) / 100.0
		} else {
			playingPct = 0.0
		}
	}

	total := 0.0
	for _, f := range fixtures {
		mult, ok := fdrMultiplier[f.FDR]
		if !ok {
			mult = 1.0
		}
		venue := awayPenalty
		if f.IsHome {
			venue = homeBoost
		}
		total += baseRate * mult * venue * playingPct
	}
	return Round(total, 2)
}

func buildPlayerSummary(p *fpl.Player, teamName string, fixtures []projectionFixture, expected float64, teams map[int]*fpl.Team) *PlayerSummary {
	sorted := slices.Clone(fixtures)
	slices.SortStableFunc(sorted, func(a, b projectionFixture) int { return a.Gameweek - b.Gameweek })

	details := make([]FixtureDetail, 0, len(sorted))
	for _, f := range sorted {
		venue := "A"
		if f.IsHome {
			venue = "H"
		}
		details = append(details, FixtureDetail{
			Gameweek: f.Gameweek,
			Opponent: fmt.Sprintf("%s(%s)", shortName(teams[f.Opponent]), venue),
			FDR:      f.FDR,
		})
	}

	status := p.Status
	if status == "" {
		status = "a"
	}

	return &PlayerSummary{
		ID:                      p.ID,
		Name:                    p.WebName,
		Team:                    teamName,
		Position:                Position(p.ElementType),
		Cost:                    float64(p.NowCost) / 10,
		Form:                    p.Form.Float(),
		PointsPerGame:           p.PointsPerGame.Float(),
		TotalPoints:             p.TotalPoints,
		Status:                  status,
		ExpectedPointsProjected: expected,
		Fixtures:                details,
	}
}

// AnalyzeHit projects both players over gameweeksAhead and reports whether the
// transfer justifies its cost. gameweeksAhead defaults to 5.
func (e *Engine) AnalyzeHit(ctx context.Context, playerOutID, playerInID, gameweeksAhead int) (*HitResult, error) {
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

	nextGW := bootstrap.NextGameweek()
	teams := teamsByID(bootstrap)

	byID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		byID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}

	playerOut, ok := byID[playerOutID]
	if !ok {
		return &HitResult{Error: fmt.Sprintf("Player with ID %d not found.", playerOutID)}, nil
	}
	playerIn, ok := byID[playerInID]
	if !ok {
		return &HitResult{Error: fmt.Sprintf("Player with ID %d not found.", playerInID)}, nil
	}

	window := buildProjectionWindow(fixtures, nextGW, gameweeksAhead)
	outFixtures := window[playerOut.Team]
	inFixtures := window[playerIn.Team]

	outExpected := projectExpectedPoints(playerOut, outFixtures)
	inExpected := projectExpectedPoints(playerIn, inFixtures)

	netGain := Round(inExpected-outExpected, 2)
	netAfterHit := Round(netGain+hitCost, 2)
	worthIt := netAfterHit > 0

	confidence, verdict := hitVerdict(playerOut, playerIn, netGain, netAfterHit, gameweeksAhead, worthIt)

	return &HitResult{
		Gameweek:           nextGW,
		GameweeksProjected: gameweeksAhead,
		PlayerOut:          buildPlayerSummary(playerOut, shortName(teams[playerOut.Team]), outFixtures, outExpected, teams),
		PlayerIn:           buildPlayerSummary(playerIn, shortName(teams[playerIn.Team]), inFixtures, inExpected, teams),
		Analysis: &HitAnalysis{
			PlayerOutExpectedPoints: outExpected,
			PlayerInExpectedPoints:  inExpected,
			ProjectedGain:           netGain,
			HitCost:                 hitCost,
			NetAfterHit:             netAfterHit,
			HitWorthIt:              worthIt,
			Confidence:              confidence,
		},
		Verdict: verdict,
	}, nil
}

// hitVerdict grades the result into a confidence band and plain-English advice.
// The bands are deliberately conservative near the break-even point, where a
// free transfer next week is usually the better play.
func hitVerdict(out, in *fpl.Player, netGain, netAfterHit float64, gws int, worthIt bool) (string, string) {
	gain := FloatStr(netGain)
	net := FloatStr(netAfterHit)

	if worthIt {
		switch {
		case netAfterHit >= 8:
			return "high", fmt.Sprintf(
				"Strongly recommended. %s is projected to outscore %s by %s points over %d GWs. "+
					"Even after the -4 hit, you gain ~%s points.",
				in.WebName, out.WebName, gain, gws, net)
		case netAfterHit >= 4:
			return "medium-high", fmt.Sprintf(
				"Recommended. The projected gain of %s points comfortably covers the -4 hit, "+
					"leaving a net benefit of ~%s points.", gain, net)
		default:
			return "medium", fmt.Sprintf(
				"Marginal but positive. The projected gain of %s points just about covers the -4 hit "+
					"(net ~%s). Consider waiting if you have a free transfer coming.", gain, net)
		}
	}

	if netAfterHit >= -2 {
		return "low", fmt.Sprintf(
			"Not recommended, but close. The projected gain of %s points doesn't quite cover the -4 hit "+
				"(net ~%s). Wait for a free transfer if possible.", gain, net)
	}
	return "low", fmt.Sprintf(
		"Not worth it. %s is only projected to outscore %s by %s points over %d GWs. "+
			"After the -4 hit, you'd lose ~%s points.",
		in.WebName, out.WebName, gain, gws, FloatStr(-netAfterHit))
}
