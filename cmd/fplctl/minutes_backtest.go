package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fantasypl/mcp/internal/algo"
	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/insights"
	"github.com/fantasypl/mcp/internal/vaastav"
)

// insightsSeason converts a vaastav season identifier ("2025-26") to
// FPL-Core-Insights' four-digit-both-years form ("2025-2026") — the two
// sources name seasons differently, verified against both live repos.
func insightsSeason(vaastavSeason string) (string, error) {
	parts := strings.SplitN(vaastavSeason, "-", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("season %q not in vaastav's YYYY-YY form", vaastavSeason)
	}
	startYear, err := strconv.Atoi(parts[0])
	if err != nil {
		return "", fmt.Errorf("season %q not in vaastav's YYYY-YY form: %w", vaastavSeason, err)
	}
	return fmt.Sprintf("%d-%d", startYear, startYear+1), nil
}

// recentStartWindow is how many prior gameweeks' lineups feed the recent
// start-rate signal that this experiment tests against the production
// season-to-date minutesCert heuristic (Starts / games-played-this-season).
const recentStartWindow = 5

// minStartWindowApps is the minimum matchday-squad appearances within the
// window before a player's recent start rate is trusted over the season
// heuristic; below this there's too little recent data to say anything, so
// the player keeps their original, season-based Starts value untouched.
const minStartWindowApps = 2

// playerStartHistory is one player's matchday-squad record within the
// recent-start window.
type playerStartHistory struct {
	starts    int
	squadApps int
}

// recentStartRates scans lineups.csv for the recentStartWindow gameweeks
// strictly before predictGW (never predictGW itself — no look-ahead) and
// returns, per FPL player id, how many of their matchday-squad appearances
// in that window were starts (lineups.csv's is_starting — see the package
// doc on why this is used instead of playermatchstats.csv's start_min/
// finish_min, the field Part B's plan originally named). insightsSeasonStr
// is FPL-Core-Insights' own season form ("2025-2026"), already converted by
// the caller — see insightsSeason.
//
// A missing gameweek file (a postponement, or a season/gameweek
// FPL-Core-Insights hasn't covered) is skipped rather than an error — a
// thinner window is still useful signal. If every gameweek in the window is
// unavailable, returns insights.ErrNotAvailable so the caller can exclude
// that gameweek from the comparison entirely, rather than compare against
// an empty signal.
func recentStartRates(ctx context.Context, ins *insights.Client, insightsSeasonStr string, predictGW int) (map[int]playerStartHistory, error) {
	out := make(map[int]playerStartHistory)
	windowStart := predictGW - recentStartWindow
	if windowStart < 1 {
		windowStart = 1
	}

	found := false
	for gw := windowStart; gw < predictGW; gw++ {
		rows, err := ins.GameweekFile(ctx, insightsSeasonStr, gw, "lineups.csv")
		if errors.Is(err, insights.ErrNotAvailable) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("lineups GW%d: %w", gw, err)
		}
		found = true
		for _, row := range rows {
			if row["player_id"] == "" {
				continue
			}
			id := insights.Int(row["player_id"])
			h := out[id]
			h.squadApps++
			if row["is_starting"] == "True" {
				h.starts++
			}
			out[id] = h
		}
	}
	if !found {
		return nil, insights.ErrNotAvailable
	}
	return out, nil
}

// buildMinutesVariantBootstrap clones c.Bootstrap and, for every player with
// at least minStartWindowApps matchday-squad appearances in the recent
// window, overrides Starts so that minutesCert (Starts / gwPlayed — gwPlayed
// computed the same way scorePlayer does, from the player's real
// season-to-date Minutes, left untouched here) equals their recent start
// rate instead of their season-to-date rate. Every other scoring term
// (xG/90, xA/90, bonusPG, ...) reads Minutes/other fields unchanged, so this
// is the same substitution-only design as the Elo experiment: zero
// internal/algo changes, only the input data differs between the baseline
// and variant runs. matched reports how many players actually got a
// recency-adjusted value, for visibility.
func buildMinutesVariantBootstrap(c *vaastav.Case, rates map[int]playerStartHistory) (*fpl.Bootstrap, int) {
	players := make([]fpl.Player, len(c.Bootstrap.Elements))
	copy(players, c.Bootstrap.Elements)

	matched := 0
	for i, p := range players {
		h, ok := rates[p.ID]
		if !ok || h.squadApps < minStartWindowApps {
			continue
		}

		nineties := 0.0
		if p.Minutes > 0 {
			nineties = float64(p.Minutes) / 90.0
		}
		gwPlayed := 1
		if nineties > 0 {
			gwPlayed = max(1, algo.RoundToInt(nineties))
		}

		rate := float64(h.starts) / float64(h.squadApps)
		players[i].Starts = algo.RoundToInt(rate * float64(gwPlayed))
		matched++
	}

	b := *c.Bootstrap
	b.Elements = players
	return &b, matched
}

// runBacktestMinutesCompare runs the unmodified captain algorithm twice per
// gameweek — once against the production season-to-date minutesCert
// heuristic, once with a recent-start-rate variant substituted in (see
// buildMinutesVariantBootstrap) — and reports both, split by tuning vs.
// held-out season. Part B's plan named this the #2-ranked feature and the
// top captaincy failure mode (rotation); this is the "measured, not
// assumed" gate before it ships.
func runBacktestMinutesCompare(ctx context.Context, root string, seasons []string, holdout string, from, to int) error {
	if from == 0 || to == 0 || to < from {
		return fmt.Errorf("minutes-compare needs -from N -to M")
	}
	if len(seasons) == 0 || (len(seasons) == 1 && seasons[0] == "") {
		return fmt.Errorf("minutes-compare needs -seasons S1,S2,... (comma-separated, no trailing comma)")
	}

	corpus := vaastav.NewCorpus(filepath.Join(root, ".cache", "vaastav"))
	ins := insights.NewClient(filepath.Join(root, ".cache", "insights"))

	var baseTuning, baseHeld, variantTuning, variantHeld []msResult
	skipped := 0
	for _, season := range seasons {
		fmt.Printf("== %s ==\n", season)
		insSeason, err := insightsSeason(season)
		if err != nil {
			return err
		}
		for gw := from; gw <= to; gw++ {
			if gw < 2 {
				continue
			}
			c, err := corpus.BuildCase(ctx, season, gw)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  skipping %s GW%d: %v\n", season, gw, err)
				continue
			}

			rates, err := recentStartRates(ctx, ins, insSeason, gw)
			if errors.Is(err, insights.ErrNotAvailable) {
				skipped++
				fmt.Printf("  skipping %s GW%d: no lineups.csv coverage in the recent window\n", season, gw)
				continue
			}
			if err != nil {
				return fmt.Errorf("%s GW%d: %w", season, gw, err)
			}

			variantBootstrap, _ := buildMinutesVariantBootstrap(c, rates)

			baseRes, err := scoreCaptainPicks(ctx, c)
			if err != nil {
				return fmt.Errorf("%s GW%d (baseline): %w", season, gw, err)
			}

			variantCase := *c
			variantCase.Bootstrap = variantBootstrap
			variantRes, err := scoreCaptainPicks(ctx, &variantCase)
			if err != nil {
				return fmt.Errorf("%s GW%d (minutes variant): %w", season, gw, err)
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

	fmt.Printf("\n(%d gameweek(s) skipped: no recent lineup coverage)\n", skipped)

	fmt.Println("\n--- Tuning seasons: baseline (season-to-date minutesCert) ---")
	printCorpusSummary(baseTuning)
	fmt.Println("\n--- Tuning seasons: recent-minutes variant ---")
	printCorpusSummary(variantTuning)
	if holdout != "" {
		fmt.Printf("\n--- Held-out season %s: baseline ---\n", holdout)
		printCorpusSummary(baseHeld)
		fmt.Printf("\n--- Held-out season %s: recent-minutes variant ---\n", holdout)
		printCorpusSummary(variantHeld)
	}
	return nil
}
