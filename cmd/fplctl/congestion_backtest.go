package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/insights"
	"github.com/fantasypl/mcp/internal/vaastav"
)

// probeMaxGW bounds the gameweek probe range for building a team's
// cross-competition match calendar.
const probeMaxGW = 38

// congestionFDRBump is how much a congested side's fixture difficulty is
// raised (clamped to FDR's 1-5 range) — the same mechanism blendFDR already
// consumes unchanged, so this experiment needs zero internal/algo changes,
// matching the Elo and minutes experiments' substitution-only design.
const congestionFDRBump = 1.0

// buildCongestionVariantFixtures clones fixtures and, for every gw fixture
// whose home or away side played within insights.ShortRestThresholdDays days
// beforehand (across every competition), bumps that side's FDR by
// congestionFDRBump. matched counts how many sides were adjusted.
func buildCongestionVariantFixtures(fixtures []fpl.Fixture, gw int, codeByTeamID map[int]int, datesByCode map[int][]time.Time) ([]fpl.Fixture, int) {
	out := make([]fpl.Fixture, len(fixtures))
	copy(out, fixtures)

	matched := 0
	for i, f := range out {
		if !f.InGameweek(gw) {
			continue
		}
		kt, err := time.Parse(time.RFC3339, f.KickoffTime)
		if err != nil {
			continue
		}

		if code, ok := codeByTeamID[f.TeamH]; ok {
			if rest, ok := insights.RestDaysBefore(datesByCode[code], kt); ok && rest <= insights.ShortRestThresholdDays {
				out[i].TeamHDifficulty = min(5, out[i].TeamHDifficulty+int(congestionFDRBump))
				matched++
			}
		}
		if code, ok := codeByTeamID[f.TeamA]; ok {
			if rest, ok := insights.RestDaysBefore(datesByCode[code], kt); ok && rest <= insights.ShortRestThresholdDays {
				out[i].TeamADifficulty = min(5, out[i].TeamADifficulty+int(congestionFDRBump))
				matched++
			}
		}
	}
	return out, matched
}

// runBacktestCongestionCompare runs the unmodified captain algorithm twice
// per gameweek — once against the production fixture list, once with
// short-rest teams' FDR bumped (see buildCongestionVariantFixtures) — and
// reports both, split by tuning vs. held-out season. Cross-competition
// fixture data is only available for 2025-26 (verified live — see
// CHANGELOG), so with one season, holdout is realistically by gameweek
// range rather than by season, same constraint the minutes-model backtest
// hit.
func runBacktestCongestionCompare(ctx context.Context, root string, seasons []string, holdout string, from, to int) error {
	if from == 0 || to == 0 || to < from {
		return fmt.Errorf("congestion-compare needs -from N -to M")
	}
	if len(seasons) == 0 || (len(seasons) == 1 && seasons[0] == "") {
		return fmt.Errorf("congestion-compare needs -seasons S1,S2,... (comma-separated, no trailing comma)")
	}

	corpus := vaastav.NewCorpus(filepath.Join(root, ".cache", "vaastav"))
	ins := insights.NewClient(filepath.Join(root, ".cache", "insights"))

	var baseTuning, baseHeld, variantTuning, variantHeld []msResult
	for _, season := range seasons {
		fmt.Printf("== %s ==\n", season)
		insSeason, err := insightsSeason(season)
		if err != nil {
			return err
		}
		fmt.Printf("  fetching cross-competition fixture calendar (%d gameweeks, cached after first run)...\n", probeMaxGW)
		datesByCode, err := ins.TeamFixtureCalendar(ctx, insSeason, 1, probeMaxGW)
		if err != nil {
			return fmt.Errorf("%s: %w", season, err)
		}
		fmt.Printf("  found calendars for %d teams\n", len(datesByCode))

		for gw := from; gw <= to; gw++ {
			if gw < 2 {
				continue
			}
			c, err := corpus.BuildCase(ctx, season, gw)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  skipping %s GW%d: %v\n", season, gw, err)
				continue
			}

			codeByTeamID := make(map[int]int, len(c.Bootstrap.Teams))
			for _, t := range c.Bootstrap.Teams {
				codeByTeamID[t.ID] = t.Code
			}

			variantFixtures, _ := buildCongestionVariantFixtures(c.Fixtures, gw, codeByTeamID, datesByCode)

			baseRes, err := scoreCaptainPicks(ctx, c)
			if err != nil {
				return fmt.Errorf("%s GW%d (baseline): %w", season, gw, err)
			}

			variantCase := *c
			variantCase.Fixtures = variantFixtures
			variantRes, err := scoreCaptainPicks(ctx, &variantCase)
			if err != nil {
				return fmt.Errorf("%s GW%d (congestion variant): %w", season, gw, err)
			}

			if season == holdout {
				baseHeld = append(baseHeld, baseRes)
				variantHeld = append(variantHeld, variantRes)
			} else {
				baseTuning = append(baseTuning, baseRes)
				variantTuning = append(variantTuning, variantRes)
			}
		}
	}

	fmt.Println("\n--- Tuning seasons: baseline (no congestion signal) ---")
	printCorpusSummary(baseTuning)
	fmt.Println("\n--- Tuning seasons: congestion variant ---")
	printCorpusSummary(variantTuning)
	if holdout != "" {
		fmt.Printf("\n--- Held-out season %s: baseline ---\n", holdout)
		printCorpusSummary(baseHeld)
		fmt.Printf("\n--- Held-out season %s: congestion variant ---\n", holdout)
		printCorpusSummary(variantHeld)
	}
	return nil
}
