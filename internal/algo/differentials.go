package algo

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/fantasypl/mcp/internal/fpl"
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
	Rank    int               `json:"rank"`
	Player  DifferentialBrief `json:"player"`
	Fixture *FixtureInfo      `json:"fixture"`
	Score   float64           `json:"score"`
	Stats   DifferentialStats `json:"stats"`
	Why     string            `json:"why"`
	Streak  Streak            `json:"streak"`
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

// buildWhy explains why this player is a differential *now* rather than in the
// abstract — ownership band, form, and what makes the coming gameweek suitable.
func buildWhy(p *fpl.Player, fixtures []TeamFixture) string {
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
			Why:    buildWhy(p, s.fixtures),
			Streak: DetectStreak(p),
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
