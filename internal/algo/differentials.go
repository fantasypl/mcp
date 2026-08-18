package algo

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/insights"
)

// Differentials surfaces under-owned players whose output is running ahead of
// their ownership — the players a rival's template squad does not have.
//
// The score is a plain weighted sum rather than the captain model's
// multiplicative form, because the question is different: captaincy asks who
// will score most this week, whereas a differential asks who is mispriced by
// the market. Ownership therefore enters as a penalty.
//
//	score = form×3.0 + ppg×1.0 − avgFDR×0.5 + ict×0.01 − ownership×0.1
//
// A double gameweek adds one point per extra fixture.

const differentialsVersion = "1.1"

type DifferentialResult struct {
	Gameweek         int            `json:"gameweek"`
	MaxOwnershipPct  float64        `json:"max_ownership_pct"`
	AlgorithmVersion string         `json:"algorithm_version"`
	NumDifferentials int            `json:"num_differentials"`
	Differentials    []Differential `json:"differentials"`
}

type Differential struct {
	Rank                int                  `json:"rank"`
	Player              DifferentialBrief    `json:"player"`
	Fixture             *FixtureInfo         `json:"fixture"`
	Score               float64              `json:"score"`
	Stats               DifferentialStats    `json:"stats"`
	Why                 string               `json:"why"`
	Streak              Streak               `json:"streak"`
	FinishingRegression *FinishingRegression `json:"finishing_regression,omitempty"`
	RoleChange          *RoleChange          `json:"role_change,omitempty"`
}

// FinishingRegression is the shot-level buy/sell signal from
// internal/insights.FinishingLuck: whether a player's on-target Premier
// League shots are converting above or below what the shot model (xGOT)
// expects. Present only when FinishingLuckSource is configured and the
// current season has shots.csv coverage (2025-26 as of this writing) — see
// Engine.FinishingLuckSource's doc. Measured via fplctl finishing-regression
// (see CHANGELOG.md): players flagged "buy" outscored players flagged
// "sell" in actual future FPL output across every split tested, so this is
// informational reasoning, not (yet) a change to Score itself — folding it
// into the ranking formula would need its own weight, backtest-justified
// separately from "the signal exists at all."
type FinishingRegression struct {
	// Delta is actual on-target goals minus summed xGOT: negative means
	// underperforming shot quality (due to regress up — buy), positive means
	// overperforming it (due to regress down — sell).
	Delta         float64 `json:"delta"`
	Signal        string  `json:"signal"` // "buy", "sell", or "neutral"
	ActualGoals   int     `json:"actual_goals"`
	ShotsOnTarget int     `json:"shots_on_target"`
}

// RoleChange is the positional-drift signal from
// internal/insights.AveragePositions/ComputePositionDrift: a player whose
// average pitch position has advanced meaningfully between an earlier
// baseline window and a recent one — a possible role change (more advanced,
// more attacking) before goals/assists/ICT catch up. Present only when
// RoleChangeSource is configured, the season has average_positions.csv
// coverage (2025-26 as of this writing), and the player isn't a goalkeeper
// — see Engine.RoleChangeSource's doc. Measured via fplctl role-change (see
// CHANGELOG.md): weaker, noisier evidence than FinishingRegression's (2 of
// 3 gameweek splits positive, one roughly flat, pooled +3.6%), so like
// FinishingRegression this is informational reasoning, not a change to
// Score — folding it into the ranking formula would need its own weight,
// backtest-justified separately, and this evidence isn't strong enough yet
// to attempt that.
type RoleChange struct {
	// DeltaX is the recent window's average advancement minus the baseline
	// window's — see internal/insights.PositionDrift.DeltaX.
	DeltaX   float64 `json:"delta_x"`
	Position string  `json:"position"`
}

type DifferentialBrief struct {
	ID            int     `json:"id"`
	Name          string  `json:"name"`
	Team          string  `json:"team"`
	TeamFullName  string  `json:"team_full_name"`
	Position      string  `json:"position"`
	Cost          float64 `json:"cost"`
	SelectedByPct float64 `json:"selected_by_pct"`
}

type DifferentialStats struct {
	Form          float64 `json:"form"`
	PointsPerGame float64 `json:"points_per_game"`
	ICTIndex      float64 `json:"ict_index"`
	TotalPoints   int     `json:"total_points"`
}

// DifferentialScore exposes differentialScore for callers outside the
// package. cmd/fplctl's evaluate and audit subcommands both inline this same
// formula directly against their own ownership/status filters (narrower than
// Differentials' — see fplctl's evaluate.go) rather than calling
// Differentials, so fplctl needs the scoring primitive on its own.
func DifferentialScore(p *fpl.Player, fixtures []TeamFixture, ownershipPct float64) float64 {
	return differentialScore(p, fixtures, ownershipPct)
}

// differentialScore weighs recent output against how many managers already own
// the player. Fixture difficulty is averaged so a double gameweek is judged on
// its overall run rather than its first match.
func differentialScore(p *fpl.Player, fixtures []TeamFixture, ownershipPct float64) float64 {
	avgFDR := 3.0
	if len(fixtures) > 0 {
		sum := 0.0
		for _, f := range fixtures {
			sum += f.FDR
		}
		avgFDR = sum / float64(len(fixtures))
	}

	score := p.Form.Float()*3.0 +
		p.PointsPerGame.Float()*1.0 -
		avgFDR*0.5 +
		p.ICTIndex.Float()*0.01 -
		ownershipPct*0.1

	if len(fixtures) > 1 {
		score += float64(len(fixtures)) * 1.0
	}

	return Round(score, 3)
}

// buildWhy explains why this player is a differential *now* rather than in
// the abstract — ownership band, form, what makes the coming gameweek
// suitable, and (when available) the finishing-regression and role-change
// signals. fr/rc are nil whenever the corresponding *Source isn't
// configured or the player doesn't qualify — existing callers that never
// set either are unaffected, since both are always nil for them.
func buildWhy(p *fpl.Player, fixtures []TeamFixture, fr *FinishingRegression, rc *RoleChange) string {
	var parts []string

	ownership := p.SelectedByPercent.Float()
	form := p.Form.Float()
	ppg := p.PointsPerGame.Float()

	// Ownership is rendered from the parsed value. FPL emits one decimal place
	// consistently, verified across the frozen payloads, so this matches the
	// source string exactly.
	own := FloatStr(ownership)
	switch {
	case ownership < 2:
		parts = append(parts, "only "+own+"% owned — massive differential")
	case ownership < 5:
		parts = append(parts, "just "+own+"% owned")
	default:
		parts = append(parts, own+"% owned")
	}

	if form >= 7 {
		parts = append(parts, "red-hot form ("+FloatStr(form)+")")
	} else if form >= 5 {
		parts = append(parts, "strong form ("+FloatStr(form)+")")
	}

	if len(fixtures) > 0 {
		sum := 0.0
		allHome := true
		for _, f := range fixtures {
			sum += f.FDR
			if !f.IsHome {
				allHome = false
			}
		}
		avgFDR := sum / float64(len(fixtures))

		if len(fixtures) > 1 {
			parts = append(parts, fmt.Sprintf("double gameweek (%d fixtures)", len(fixtures)))
		} else if avgFDR <= 2 {
			parts = append(parts, "easy fixture ahead")
		}
		if allHome {
			parts = append(parts, "playing at home")
		}
	}

	if ppg >= 5 && ownership < 5 {
		parts = append(parts, "PPG "+FloatStr(ppg)+" massively underowned for output")
	}

	if orderOf(p.CornersAndIndirectFreekicksOrder) == 1 {
		parts = append(parts, "on set pieces")
	}
	if orderOf(p.PenaltiesOrder) == 1 {
		parts = append(parts, "on penalties")
	}

	if fr != nil {
		sumXGOT := float64(fr.ActualGoals) - fr.Delta
		switch fr.Signal {
		case "buy":
			parts = append(parts, fmt.Sprintf("shot data suggests finishing is due to improve (%d goals from %s xGOT on target)", fr.ActualGoals, FloatStr(Round(sumXGOT, 1))))
		case "sell":
			parts = append(parts, "shot data suggests recent finishing is running above shot quality")
		}
	}

	if rc != nil {
		parts = append(parts, fmt.Sprintf("average position has pushed %s yards further forward recently — possible role change", FloatStr(rc.DeltaX)))
	}

	return Capitalize(strings.Join(parts, ". "))
}

// Differentials returns under-owned players ranked by differential score.
//
// maxOwnershipPct caps how widely owned a candidate may be. gameweek is
// optional; nil selects the next one. topN defaults to 10.
func (e *Engine) Differentials(ctx context.Context, maxOwnershipPct float64, gameweek *int, topN int) (*DifferentialResult, error) {
	if topN <= 0 {
		topN = 10
	}

	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	fixtures, err := e.client.Fixtures(ctx)
	if err != nil {
		return nil, err
	}

	gw := bootstrap.NextGameweek()
	if gameweek != nil {
		gw = *gameweek
	}

	teams := teamsByID(bootstrap)
	fixtureMap := buildFixtureMap(fixtures, gw, teams)
	finishingLuck := e.finishingLuckMap(ctx, bootstrap)
	roleChange := e.roleChangeMap(ctx, bootstrap)

	scored := make([]scoredPlayer, 0, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		p := &bootstrap.Elements[i]

		ownership := p.SelectedByPercent.Float()
		if ownership > maxOwnershipPct {
			continue
		}
		if InjuryStatuses[p.Status] {
			continue
		}
		pf := fixtureMap[p.Team]
		if len(pf) == 0 {
			continue // blanking this gameweek
		}
		scored = append(scored, scoredPlayer{differentialScore(p, pf, ownership), p, pf})
	}

	// Stable ordering keeps tied candidates in squad-list order rather than
	// shuffling between runs.
	slices.SortStableFunc(scored, func(a, b scoredPlayer) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		default:
			return 0
		}
	})

	results := make([]Differential, 0, topN)
	for i := 0; i < len(scored) && i < topN; i++ {
		s := scored[i]
		p := s.player
		team := teams[p.Team]
		fr := finishingRegressionFor(finishingLuck[p.ID])
		rc := roleChangeFor(roleChange[p.ID])

		results = append(results, Differential{
			Rank: len(results) + 1,
			Player: DifferentialBrief{
				ID:            p.ID,
				Name:          p.WebName,
				Team:          shortName(team),
				TeamFullName:  fullName(team),
				Position:      Position(p.ElementType),
				Cost:          float64(p.NowCost) / 10,
				SelectedByPct: p.SelectedByPercent.Float(),
			},
			Fixture: fixtureInfoFor(s.fixtures, gw, teams),
			Score:   s.score,
			Stats: DifferentialStats{
				Form:          p.Form.Float(),
				PointsPerGame: p.PointsPerGame.Float(),
				ICTIndex:      p.ICTIndex.Float(),
				TotalPoints:   p.TotalPoints,
			},
			Why:                 buildWhy(p, s.fixtures, fr, rc),
			Streak:              DetectStreak(p),
			FinishingRegression: fr,
			RoleChange:          rc,
		})
	}

	return &DifferentialResult{
		Gameweek:         gw,
		MaxOwnershipPct:  maxOwnershipPct,
		AlgorithmVersion: differentialsVersion,
		NumDifferentials: len(results),
		Differentials:    results,
	}, nil
}

// finishingLuckMap best-effort fetches the finishing-regression signal for
// the current season through the last fully-finished gameweek. Returns nil
// (no signal for anyone) if FinishingLuckSource isn't configured, there's no
// finished gameweek yet, or the fetch fails for any reason — this is
// enrichment, never allowed to break Differentials itself. e.Now(), not
// time.Now(), so tests that inject a fixed clock get a deterministic season
// string too.
func (e *Engine) finishingLuckMap(ctx context.Context, bootstrap *fpl.Bootstrap) map[int]insights.FinishingDelta {
	if e.FinishingLuckSource == nil {
		return nil
	}
	// CurrentGameweek() can be a gameweek still in progress; shots.csv for it
	// may be incomplete (the source refreshes twice daily, not live), so this
	// only aggregates through the gameweek before it — the same no-look-ahead
	// discipline internal/vaastav uses for backtesting.
	throughGW := bootstrap.CurrentGameweek() - 1
	if throughGW < 1 {
		return nil
	}
	luck, err := e.FinishingLuckSource.FinishingLuck(ctx, currentInsightsSeason(e.Now()), 1, throughGW)
	if err != nil {
		return nil
	}
	return luck
}

// currentInsightsSeason derives FPL-Core-Insights' season identifier
// ("2025-2026") from now, using the Premier League's own August season
// boundary.
func currentInsightsSeason(now time.Time) string {
	y := now.Year()
	if now.Month() < time.August {
		y--
	}
	return fmt.Sprintf("%d-%d", y, y+1)
}

// finishingRegressionFor converts a raw FinishingDelta into the "buy"/"sell"
// signal Differentials surfaces, or nil if fl doesn't qualify (including the
// zero value a missing map entry produces — a player with no shots this
// season is indistinguishable from "not enough shots to trust," which is
// the correct behavior either way).
func finishingRegressionFor(fl insights.FinishingDelta) *FinishingRegression {
	if !fl.Qualified() {
		return nil
	}
	delta := Round(fl.Delta(), 2)
	signal := "neutral"
	switch {
	case delta <= -1.0:
		signal = "buy"
	case delta >= 1.0:
		signal = "sell"
	}
	return &FinishingRegression{
		Delta: delta, Signal: signal, ActualGoals: fl.ActualGoals, ShotsOnTarget: fl.ShotsOnTarget,
	}
}

// roleChangeRecentWindow is how many of the most recent gameweeks form the
// "recent" window for the positional-drift signal — matches the recent
// window (GW11-20) from the fplctl role-change split with the strongest
// validated result (see CHANGELOG.md).
const roleChangeRecentWindow = 10

// roleChangeMinMatches and roleChangeDeltaThreshold are the qualification
// bar validated via fplctl role-change -min-matches 5 -exclude-gk: fewer
// matches in either window is dominated by cameo-appearance noise (see
// CHANGELOG.md's refuted low-minutes-floor and GK-exclusion hypotheses —
// raising the match count was the one lever that actually helped).
// roleChangeDeltaThreshold (yards of average-position advancement) is set
// near the ~15th-percentile cutoff observed across the three validated
// splits (6.1-7.3), i.e. roughly the same slice of the qualifying pool the
// backtest's "advanced" group was drawn from.
const (
	roleChangeMinMatches     = 5
	roleChangeDeltaThreshold = 6.0
)

// roleChangeMap best-effort fetches the positional-drift signal for the
// current season: a baseline window (season start through the gameweek
// before the recent window) and a recent window
// (roleChangeRecentWindow gameweeks, through the last fully-finished
// gameweek — the same no-look-ahead cutoff finishingLuckMap uses). Returns
// nil (no signal for anyone) if RoleChangeSource isn't configured, there
// isn't yet enough season history for both windows, or either fetch fails —
// this is enrichment, never allowed to break Differentials itself.
func (e *Engine) roleChangeMap(ctx context.Context, bootstrap *fpl.Bootstrap) map[int]insights.PositionDrift {
	if e.RoleChangeSource == nil {
		return nil
	}
	throughGW := bootstrap.CurrentGameweek() - 1
	recentFrom := throughGW - roleChangeRecentWindow + 1
	if recentFrom < 2 {
		return nil // not enough season history yet for a baseline window
	}
	season := currentInsightsSeason(e.Now())
	baseline, err := e.RoleChangeSource.AveragePositions(ctx, season, 1, recentFrom-1)
	if err != nil {
		return nil
	}
	recent, err := e.RoleChangeSource.AveragePositions(ctx, season, recentFrom, throughGW)
	if err != nil {
		return nil
	}
	return insights.ComputePositionDrift(baseline, recent)
}

// roleChangeFor converts a raw PositionDrift into the role-change signal
// Differentials surfaces, or nil if it doesn't qualify: too few matches in
// either window, a goalkeeper (whose average position is structurally
// near-static — see CHANGELOG.md), or drift below roleChangeDeltaThreshold.
// Only positive drift (advancing) is surfaced — the backtest validated an
// "advanced" cohort against a stable-position control, not a "dropped"
// cohort, so a negative DeltaX isn't a signal this function can vouch for.
func roleChangeFor(d insights.PositionDrift) *RoleChange {
	if d.Position == "G" {
		return nil
	}
	if d.BaselineMatches < roleChangeMinMatches || d.RecentMatches < roleChangeMinMatches {
		return nil
	}
	delta := Round(d.DeltaX(), 1)
	if delta < roleChangeDeltaThreshold {
		return nil
	}
	return &RoleChange{DeltaX: delta, Position: d.Position}
}

// fixtureInfoFor renders a team's fixtures for a gameweek, with the first
// fixture also promoted to top-level fields for callers that predate double
// gameweek support.
func fixtureInfoFor(fixtures []TeamFixture, gw int, teams map[int]*fpl.Team) *FixtureInfo {
	if len(fixtures) == 0 {
		return nil
	}
	entries := make([]FixtureEntry, 0, len(fixtures))
	for _, f := range fixtures {
		venue := "Away"
		if f.IsHome {
			venue = "Home"
		}
		entries = append(entries, FixtureEntry{
			Opponent: shortName(teams[f.Opponent]),
			Venue:    venue,
			FDR:      f.FDR,
		})
	}
	return &FixtureInfo{
		Fixtures: entries,
		Gameweek: gw,
		IsDGW:    len(entries) > 1,
		Opponent: entries[0].Opponent,
		Venue:    entries[0].Venue,
		FDR:      entries[0].FDR,
	}
}
