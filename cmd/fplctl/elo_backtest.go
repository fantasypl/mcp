package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fantasypl/mcp/internal/clubelo"
	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/vaastav"
)

// eloScaledStrength converts a ClubElo rating into the numeric range FPL's
// own team-strength fields use (roughly 1000-1400), via the inverse of the
// exact linear transform blendFDR already applies when normalising strength
// to a 1-5 difficulty scale:
//
//	FPL:  strengthNorm = clamp(1,5, (strength-1000)/100 + 1)
//	Elo:  eloNorm      = clamp(1,5, (elo-1500)/150 + 1)   (chosen to span PL
//	                     clubs' observed Elo range, ~1500 weakest to ~2100
//	                     strongest, over the same 1-5 spread)
//
// Solving strengthNorm's formula for the "strength" that reproduces eloNorm
// gives strength = 1000 + (elo-1500)*100/150. Feeding that back into the
// unmodified blendFDR reproduces eloNorm exactly — so this experiment runs
// the production captain algorithm completely unmodified; only the input
// data differs between the baseline and Elo-variant runs.
func eloScaledStrength(elo float64) int {
	return int(1000 + (elo-1500)*100.0/150.0)
}

// eloVariantMode selects how an Elo-derived strength combines with a team's
// existing FPL-strength value.
type eloVariantMode int

const (
	// eloReplace substitutes the Elo-derived value outright — a clean test
	// of Elo as a standalone alternative to FPL's own dynamic strength.
	eloReplace eloVariantMode = iota
	// eloBlend averages the Elo-derived value with the existing FPL
	// strength — a test of Elo as incremental signal on top of the blend
	// already in production, per the plan's own framing.
	eloBlend
)

// buildEloVariantBootstrap clones c.Bootstrap and, for every team ClubElo
// can resolve (see clubelo.SlugFor) and has history for as of refDate,
// combines an Elo-derived value (see eloScaledStrength) into
// StrengthDefenceHome/Away — the only two fields blendFDR reads — per mode.
// A team ClubElo can't resolve or has no history before refDate keeps its
// original FPL-strength value, so a partial Elo picture never silently
// zeroes a team's fixture difficulty; matched reports how many of the 20
// teams got an Elo value, so a case with too little coverage can be
// excluded from the comparison rather than compared unfairly.
func buildEloVariantBootstrap(ctx context.Context, elo *clubelo.Client, c *vaastav.Case, refDate string, mode eloVariantMode) (*fpl.Bootstrap, int, error) {
	teams := make([]fpl.Team, len(c.Bootstrap.Teams))
	copy(teams, c.Bootstrap.Teams)

	matched := 0
	for i, t := range teams {
		slug, ok := clubelo.SlugFor(t.ShortName)
		if !ok {
			continue
		}
		r, found, err := elo.ByDate(ctx, slug, refDate)
		if err != nil {
			return nil, 0, fmt.Errorf("elo for %s: %w", t.Name, err)
		}
		if !found {
			continue
		}
		eloStrength := eloScaledStrength(r.Elo)

		s := eloStrength
		if mode == eloBlend {
			s = (teams[i].StrengthDefenceHome + teams[i].StrengthDefenceAway + 2*eloStrength) / 4
		}
		teams[i].StrengthDefenceHome = s
		teams[i].StrengthDefenceAway = s
		matched++
	}

	b := *c.Bootstrap
	b.Teams = teams
	return &b, matched, nil
}

// earliestKickoffDate returns "YYYY-MM-DD" for gw's earliest fixture — the
// reference date to fetch Elo as-of, matching what a manager would have
// known going into the gameweek. No look-ahead: later fixtures in the same
// gameweek are ignored even though technically already reconstructable.
func earliestKickoffDate(fixtures []fpl.Fixture, gw int) (string, bool) {
	var best string
	for _, f := range fixtures {
		if !f.InGameweek(gw) || len(f.KickoffTime) < 10 {
			continue
		}
		d := f.KickoffTime[:10]
		if best == "" || d < best {
			best = d
		}
	}
	return best, best != ""
}

// minEloCoverage is the minimum number of the 20 Premier League teams that
// must resolve an Elo value for a gameweek to be included in the
// comparison — below this, ClubElo's history window (see internal/clubelo's
// package doc) likely doesn't reach this date, and comparing an
// almost-entirely-FPL-strength "variant" against the true baseline would
// not be a fair test of Elo.
const minEloCoverage = 15

// runBacktestEloCompare runs the unmodified captain algorithm twice per
// gameweek — once against FPL's own dynamic team strength (today's
// production input) and once against an Elo-derived substitute (see
// eloScaledStrength) — and reports both, split by tuning vs. held-out
// season. This is Part B's "measured, not assumed" gate for the plan's
// highest-ranked candidate feature: Elo only ships if it actually beats the
// current FPL-strength blend out-of-sample, not because it's more granular
// in principle.
func runBacktestEloCompare(ctx context.Context, root string, seasons []string, holdout string, from, to int) error {
	if from == 0 || to == 0 || to < from {
		return fmt.Errorf("elo-compare needs -from N -to M")
	}
	if len(seasons) == 0 || (len(seasons) == 1 && seasons[0] == "") {
		return fmt.Errorf("elo-compare needs -seasons S1,S2,... (comma-separated, no trailing comma)")
	}

	corpus := vaastav.NewCorpus(filepath.Join(root, ".cache", "vaastav"))
	eloClient := clubelo.NewClient(filepath.Join(root, ".cache", "clubelo"))

	var baseTuning, baseHeld []msResult
	var replaceTuning, replaceHeld []msResult
	var blendTuning, blendHeld []msResult
	skipped := 0
	for _, season := range seasons {
		fmt.Printf("== %s ==\n", season)
		for gw := from; gw <= to; gw++ {
			if gw < 2 {
				continue
			}
			c, err := corpus.BuildCase(ctx, season, gw)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  skipping %s GW%d: %v\n", season, gw, err)
				continue
			}

			refDate, ok := earliestKickoffDate(c.Fixtures, gw)
			if !ok {
				fmt.Fprintf(os.Stderr, "  skipping %s GW%d: no kickoff date to anchor Elo lookup\n", season, gw)
				continue
			}

			replaceBootstrap, matched, err := buildEloVariantBootstrap(ctx, eloClient, c, refDate, eloReplace)
			if err != nil {
				return fmt.Errorf("%s GW%d: %w", season, gw, err)
			}
			if matched < minEloCoverage {
				skipped++
				fmt.Printf("  skipping %s GW%d: only %d/20 teams had Elo as of %s (outside ClubElo's history window)\n",
					season, gw, matched, refDate)
				continue
			}
			blendBootstrap, _, err := buildEloVariantBootstrap(ctx, eloClient, c, refDate, eloBlend)
			if err != nil {
				return fmt.Errorf("%s GW%d: %w", season, gw, err)
			}

			baseRes, err := scoreCaptainPicks(ctx, c)
			if err != nil {
				return fmt.Errorf("%s GW%d (baseline): %w", season, gw, err)
			}

			replaceCase := *c
			replaceCase.Bootstrap = replaceBootstrap
			replaceRes, err := scoreCaptainPicks(ctx, &replaceCase)
			if err != nil {
				return fmt.Errorf("%s GW%d (elo replace): %w", season, gw, err)
			}

			blendCase := *c
			blendCase.Bootstrap = blendBootstrap
			blendRes, err := scoreCaptainPicks(ctx, &blendCase)
			if err != nil {
				return fmt.Errorf("%s GW%d (elo blend): %w", season, gw, err)
			}

			if season == holdout {
				baseHeld = append(baseHeld, baseRes)
				replaceHeld = append(replaceHeld, replaceRes)
				blendHeld = append(blendHeld, blendRes)
			} else {
				baseTuning = append(baseTuning, baseRes)
				replaceTuning = append(replaceTuning, replaceRes)
				blendTuning = append(blendTuning, blendRes)
			}
		}
	}

	fmt.Printf("\n(%d gameweek(s) skipped: outside ClubElo's Elo-history coverage window)\n", skipped)

	fmt.Println("\n--- Tuning seasons: baseline (FPL dynamic strength) ---")
	printCorpusSummary(baseTuning)
	fmt.Println("\n--- Tuning seasons: Elo replace ---")
	printCorpusSummary(replaceTuning)
	fmt.Println("\n--- Tuning seasons: Elo blend (avg with FPL strength) ---")
	printCorpusSummary(blendTuning)
	if holdout != "" {
		fmt.Printf("\n--- Held-out season %s: baseline ---\n", holdout)
		printCorpusSummary(baseHeld)
		fmt.Printf("\n--- Held-out season %s: Elo replace ---\n", holdout)
		printCorpusSummary(replaceHeld)
		fmt.Printf("\n--- Held-out season %s: Elo blend ---\n", holdout)
		printCorpusSummary(blendHeld)
	}
	return nil
}
