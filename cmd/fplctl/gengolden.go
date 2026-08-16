package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/algo"
	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

const syntheticTeamID = 999001

func runGenGolden(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("gengolden", flag.ContinueOnError)
	which := fs.String("which", "all", "golden set: basic, live, or all")
	out := fs.String("out", "", "output directory (defaults to testdata)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *which != "basic" && *which != "live" && *which != "chips" && *which != "league" && *which != "rivals" && *which != "all" {
		return fmt.Errorf("--which must be basic, live, chips, league, rivals, or all")
	}
	root := "testdata"
	if *out != "" {
		root = *out
	}
	if *which == "basic" || *which == "all" {
		if err := genBasic(ctx, root); err != nil {
			return err
		}
	}
	if *which == "live" || *which == "all" {
		if err := genLive(ctx, root); err != nil {
			return err
		}
	}
	if *which == "chips" || *which == "all" {
		if err := genChips(ctx, root); err != nil {
			return err
		}
	}
	if *which == "league" || *which == "all" {
		if err := genLeague(ctx, root); err != nil {
			return err
		}
	}
	if *which == "rivals" || *which == "all" {
		if err := genRivals(ctx, root); err != nil {
			return err
		}
	}
	return nil
}

func readFixture[T any](path string) (T, error) {
	var v T
	b, err := os.ReadFile(path)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(b, &v)
	return v, err
}
func writeGolden(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

func basicEngine(fixture string) (*algo.Engine, error) {
	b, err := readFixture[*fpl.Bootstrap](filepath.Join("testdata", "bootstrap_"+fixture+".json"))
	if err != nil {
		return nil, err
	}
	f, err := readFixture[[]fpl.Fixture](filepath.Join("testdata", "fixtures.json"))
	if err != nil {
		return nil, err
	}
	c := algo.NewStubClient(b, f)
	p, err := readFixture[*fpl.TeamPicks](filepath.Join("testdata", "picks_squad1.json"))
	if err != nil {
		return nil, err
	}
	c.SetTeamPicks(syntheticTeamID, 1, p)
	for _, id := range []int{411, 426} {
		summary, err := readFixture[*fpl.PlayerSummary](filepath.Join("testdata", fmt.Sprintf("player_summary_%d.json", id)))
		if err != nil {
			return nil, err
		}
		c.SetPlayerSummary(id, summary)
	}
	e := algo.NewEngine(c)
	e.Now = func() time.Time { return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC) }
	return e, nil
}

func genBasic(ctx context.Context, out string) error {
	cases := []struct {
		name, fixture string
		run           func(context.Context, *algo.Engine) (any, error)
	}{
		{"captain_gw1", "", func(c context.Context, e *algo.Engine) (any, error) { return e.CaptainPicks(c, ptrInt(1), 0) }},
		{"captain_default", "", func(c context.Context, e *algo.Engine) (any, error) { return e.CaptainPicks(c, nil, 0) }},
		{"captain_top10", "", func(c context.Context, e *algo.Engine) (any, error) { return e.CaptainPicks(c, ptrInt(1), 10) }},
		{"differentials_10pct", "", func(c context.Context, e *algo.Engine) (any, error) { return e.Differentials(c, 10, ptrInt(1), 0) }},
		{"differentials_5pct", "", func(c context.Context, e *algo.Engine) (any, error) { return e.Differentials(c, 5, ptrInt(1), 0) }},
		{"fixtures_5gw", "", func(c context.Context, e *algo.Engine) (any, error) { return e.FixtureOutlook(c, 5, "") }},
		{"fixtures_3gw_mid", "", func(c context.Context, e *algo.Engine) (any, error) { return e.FixtureOutlook(c, 3, "MID") }},
		{"prices", "", func(c context.Context, e *algo.Engine) (any, error) { return e.PricePredictions(c, 0) }},
		{"hit_swap_fwd_mid", "", func(c context.Context, e *algo.Engine) (any, error) { return e.AnalyzeHit(c, 411, 426, 0) }},
		{"hit_swap_mid_def", "", func(c context.Context, e *algo.Engine) (any, error) { return e.AnalyzeHit(c, 397, 4, 3) }},
		{"transfers_1ft", "", func(c context.Context, e *algo.Engine) (any, error) {
			return e.TransferSuggestions(c, syntheticTeamID, 1, .5)
		}},
		{"transfers_2ft", "", func(c context.Context, e *algo.Engine) (any, error) {
			return e.TransferSuggestions(c, syntheticTeamID, 2, 0)
		}},
		{"scout", "", func(c context.Context, e *algo.Engine) (any, error) { return e.SquadScout(c, syntheticTeamID) }},
		{"compare_haaland_fernandes", "", func(c context.Context, e *algo.Engine) (any, error) {
			return e.ComparePlayers(c, []string{"Haaland", "B.Fernandes"}, 4)
		}},
		{"compare_not_enough_names", "", func(c context.Context, e *algo.Engine) (any, error) {
			return e.ComparePlayers(c, []string{"Haaland"}, 4)
		}},
		{"compare_no_match", "", func(c context.Context, e *algo.Engine) (any, error) {
			return e.ComparePlayers(c, []string{"Haaland", "Nonexistentplayerxyz"}, 4)
		}},
	}
	for _, fx := range []struct{ n, s string }{{"preseason", ""}, {"midseason", "_mid"}} {
		e, err := basicEngine(fx.n)
		if err != nil {
			return err
		}
		for _, tc := range cases {
			v, err := tc.run(ctx, e)
			if err != nil {
				return fmt.Errorf("%s%s: %w", tc.name, fx.s, err)
			}
			if err := writeGolden(filepath.Join(out, "golden", tc.name+fx.s+".json"), v); err != nil {
				return err
			}
		}
	}
	return nil
}
func ptrInt(v int) *int { return &v }

func genLive(ctx context.Context, out string) error {
	dir := filepath.Join("testdata", "live_scenario")
	b, err := readFixture[*fpl.Bootstrap](filepath.Join(dir, "bootstrap.json"))
	if err != nil {
		return err
	}
	f, err := readFixture[[]fpl.Fixture](filepath.Join(dir, "fixtures.json"))
	if err != nil {
		return err
	}
	p, err := readFixture[*fpl.TeamPicks](filepath.Join(dir, "picks.json"))
	if err != nil {
		return err
	}
	l, err := readFixture[*fpl.LiveResponse](filepath.Join(dir, "live.json"))
	if err != nil {
		return err
	}
	s, err := readFixture[*fpl.EventStatusResponse](filepath.Join(dir, "event_status.json"))
	if err != nil {
		return err
	}
	c := algo.NewStubClient(b, f)
	c.SetTeamPicks(syntheticTeamID, 1, p)
	c.SetLive(1, l)
	c.SetEventStatus(s)
	e := algo.NewEngine(c)
	e.Now = func() time.Time { return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC) }
	v, err := e.LivePoints(ctx, syntheticTeamID)
	if err != nil {
		return err
	}
	return writeGolden(filepath.Join(out, "live_scenario", "golden.json"), v)
}
