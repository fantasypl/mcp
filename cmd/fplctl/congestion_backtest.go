package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/insights"
	"github.com/fantasypl/mcp/internal/vaastav"
)

// probeMaxGW bounds the gameweek probe range for building a team's
// cross-competition match calendar.
const probeMaxGW = 38

// congestionRestThreshold: a team playing again within this many days of its
// previous competitive fixture, across all competitions, is "congested."
// Three days (e.g. a Tuesday Champions League tie before a Saturday
// Premier League match is 4 days' rest, a Wednesday tie is 3) is the
// standard short-turnaround threshold in football fixture analysis.
const congestionRestThreshold = 3.0

// congestionFDRBump is how much a congested side's fixture difficulty is
// raised (clamped to FDR's 1-5 range) — the same mechanism blendFDR already
// consumes unchanged, so this experiment needs zero internal/algo changes,
// matching the Elo and minutes experiments' substitution-only design.
const congestionFDRBump = 1.0

// teamFixtureDatesByCode fetches every gameweek's combined "By Gameweek"
// fixtures.csv for insSeason and returns each team's sorted match kickoff
// times, keyed by FPL's stable team code — not the season-specific team id,
// the only identifier a cross-competition fixtures.csv carries (the
// opponent side is blank when it's not an FPL-tracked club).
//
// "By Gameweek" (as opposed to "By Tournament/{competition}") is
// deliberate: verified live, it already merges every competition's fixtures
// for that gameweek into one file (e.g. GW4 carries Premier League,
// Champions League, and EFL Cup rows together), so this needs one request
// per gameweek (38) rather than one per competition per gameweek (190) —
// the difference between a probe that reliably completes and one that
// reliably trips raw.githubusercontent.com's rate limit.
func teamFixtureDatesByCode(ctx context.Context, ins *insights.Client, insSeason string) (map[int][]time.Time, error) {
	dates := make(map[int][]time.Time)
	for gw := 1; gw <= probeMaxGW; gw++ {
		rows, err := ins.GameweekFile(ctx, insSeason, gw, "fixtures.csv")
		if errors.Is(err, insights.ErrNotAvailable) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("GW%d fixtures: %w", gw, err)
		}
		for _, row := range rows {
			kt, err := time.Parse(time.RFC3339, row["kickoff_time"])
			if err != nil {
				continue
			}
			for _, side := range []string{"home_team", "away_team"} {
				if row[side] == "" {
					continue
				}
				code := insights.Int(row[side])
				if code == 0 {
					continue
				}
				dates[code] = append(dates[code], kt)
			}
		}
	}
	for code := range dates {
		sort.Slice(dates[code], func(i, j int) bool { return dates[code][i].Before(dates[code][j]) })
	}
	return dates, nil
}

// daysRestBefore returns how many days before ref the latest date strictly
// earlier than ref falls, and false if there is no such date (no known
// prior fixture — e.g. the team's season-opening match).
func daysRestBefore(dates []time.Time, ref time.Time) (float64, bool) {
	var prev time.Time
	found := false
	for _, d := range dates {
		if !d.Before(ref) {
			break
		}
		prev, found = d, true
	}
	if !found {
		return 0, false
	}
	return ref.Sub(prev).Hours() / 24, true
}

// buildCongestionVariantFixtures clones fixtures and, for every gw fixture
// whose home or away side played within congestionRestThreshold days
// beforehand (across all of congestionCompetitions), bumps that side's FDR
// by congestionFDRBump. matched counts how many sides were adjusted.
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
			if rest, ok := daysRestBefore(datesByCode[code], kt); ok && rest <= congestionRestThreshold {
				out[i].TeamHDifficulty = min(5, out[i].TeamHDifficulty+int(congestionFDRBump))
				matched++
			}
		}
		if code, ok := codeByTeamID[f.TeamA]; ok {
			if rest, ok := daysRestBefore(datesByCode[code], kt); ok && rest <= congestionRestThreshold {
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
		datesByCode, err := teamFixtureDatesByCode(ctx, ins, insSeason)
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
