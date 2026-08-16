package algo

import (
	"context"
	"testing"

	"github.com/ajitem/fpl-intelligence/internal/golden"
)

func TestDifferentialsMatchesGolden(t *testing.T) {
	bothFixtures(t, func(t *testing.T, e *Engine, suffix string) {
		for _, tc := range []struct {
			name   string
			maxOwn float64
			golden string
		}{
			{"10 percent", 10.0, "differentials_10pct"},
			{"5 percent", 5.0, "differentials_5pct"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := e.Differentials(context.Background(), tc.maxOwn, ptr(1), 0)
				if err != nil {
					t.Fatal(err)
				}
				golden.Assert(t, goldenPath(tc.golden+suffix), got)
			})
		}
	})
}

func TestPricePredictionsMatchesGolden(t *testing.T) {
	bothFixtures(t, func(t *testing.T, e *Engine, suffix string) {
		got, err := e.PricePredictions(context.Background(), 0)
		if err != nil {
			t.Fatal(err)
		}
		golden.Assert(t, goldenPath("prices"+suffix), got)
	})
}

func TestAnalyzeHitMatchesGolden(t *testing.T) {
	bothFixtures(t, func(t *testing.T, e *Engine, suffix string) {
		for _, tc := range []struct {
			name           string
			out, in, ahead int
			golden         string
		}{
			// 411 Haaland (FWD), 426 B.Fernandes (MID)
			{"forward for midfielder", 411, 426, 0, "hit_swap_fwd_mid"},
			// 397 Semenyo (MID), 4 Gabriel (DEF), 3 gameweek window
			{"midfielder for defender", 397, 4, 3, "hit_swap_mid_def"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := e.AnalyzeHit(context.Background(), tc.out, tc.in, tc.ahead)
				if err != nil {
					t.Fatal(err)
				}
				golden.Assert(t, goldenPath(tc.golden+suffix), got)
			})
		}
	})
}

// The ownership ceiling is a hard filter, not a ranking hint.
func TestDifferentialsRespectOwnershipCeiling(t *testing.T) {
	bothFixtures(t, func(t *testing.T, e *Engine, _ string) {
		for _, maxOwn := range []float64{1.0, 5.0, 10.0} {
			res, err := e.Differentials(context.Background(), maxOwn, ptr(1), 50)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range res.Differentials {
				if d.Player.SelectedByPct > maxOwn {
					t.Errorf("%s at %.1f%% exceeds ceiling %.1f%%",
						d.Player.Name, d.Player.SelectedByPct, maxOwn)
				}
			}
		}
	})
}

// A tighter ownership ceiling can only ever yield a subset of a looser one.
//
// The limit must exceed the squad size: this property holds for the candidate
// pool, not for a truncated ranking. Cap both lists and a low-scoring player
// who fits inside the smaller pool gets squeezed out of the larger one, which
// looks like a violation but is only truncation.
func TestTighterCeilingYieldsSubset(t *testing.T) {
	e := newEngine(t, "midseason")
	const noLimit = 10_000

	wide, err := e.Differentials(context.Background(), 10.0, ptr(1), noLimit)
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := e.Differentials(context.Background(), 2.0, ptr(1), noLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(narrow.Differentials) >= len(wide.Differentials) {
		t.Fatalf("narrow (%d) should be smaller than wide (%d); the ceilings are not discriminating",
			len(narrow.Differentials), len(wide.Differentials))
	}

	inWide := map[int]bool{}
	for _, d := range wide.Differentials {
		inWide[d.Player.ID] = true
	}
	for _, d := range narrow.Differentials {
		if !inWide[d.Player.ID] {
			t.Errorf("%s appears at 2%% but not at 10%%", d.Player.Name)
		}
	}
}

func TestPriceDirectionsAreConsistent(t *testing.T) {
	bothFixtures(t, func(t *testing.T, e *Engine, _ string) {
		res, err := e.PricePredictions(context.Background(), 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res.LikelyRisers {
			if r.NetTransfersGW <= 0 || r.Direction != "rise" {
				t.Errorf("riser %s has net %d direction %q", r.Player.Name, r.NetTransfersGW, r.Direction)
			}
			if r.Confidence < 0 || r.Confidence > 100 {
				t.Errorf("confidence %d out of range", r.Confidence)
			}
		}
		for _, f := range res.LikelyFallers {
			if f.NetTransfersGW >= 0 || f.Direction != "fall" {
				t.Errorf("faller %s has net %d direction %q", f.Player.Name, f.NetTransfersGW, f.Direction)
			}
		}
	})
}

// Preseason has no transfer activity, so both lists are empty. Documented so a
// future empty result is recognised as expected rather than a regression.
func TestPreseasonHasNoPriceMovement(t *testing.T) {
	e := newEngine(t, "preseason")
	res, err := e.PricePredictions(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.LikelyRisers) != 0 || len(res.LikelyFallers) != 0 {
		t.Skip("transfer activity has begun; this fixture is no longer preseason")
	}
}

func TestAnalyzeHitUnknownPlayer(t *testing.T) {
	e := newEngine(t, "preseason")
	for _, tc := range []struct{ out, in int }{
		{999999, 411},
		{411, 999999},
	} {
		res, err := e.AnalyzeHit(context.Background(), tc.out, tc.in, 5)
		if err != nil {
			t.Fatal(err)
		}
		if res.Error == "" {
			t.Errorf("AnalyzeHit(%d, %d) should report a missing player", tc.out, tc.in)
		}
		if res.Analysis != nil {
			t.Error("an errored result should carry no analysis")
		}
	}
}

// A flagged player with no stated chance is assumed out, so his projection
// collapses to zero rather than being optimistically counted.
func TestInjuredWithoutChanceProjectsZero(t *testing.T) {
	p := newPlayer()
	p.Status = "i"
	p.ChanceOfPlayingNextRound = nil

	fixtures := []projectionFixture{{Gameweek: 1, FDR: 2, IsHome: true}}
	if got := projectExpectedPoints(p, fixtures); got != 0 {
		t.Errorf("projection = %v, want 0", got)
	}

	p.ChanceOfPlayingNextRound = ptr(50)
	half := projectExpectedPoints(p, fixtures)

	p.Status = "a"
	p.ChanceOfPlayingNextRound = nil
	full := projectExpectedPoints(p, fixtures)

	if half <= 0 || half >= full {
		t.Errorf("50%% chance projection %v should sit between 0 and %v", half, full)
	}
}

func TestHitVerdictBands(t *testing.T) {
	out, in := newPlayer(), newPlayer()
	out.WebName, in.WebName = "Out", "In"

	cases := []struct {
		netAfterHit float64
		worthIt     bool
		want        string
	}{
		{10, true, "high"},
		{8, true, "high"},
		{5, true, "medium-high"},
		{1, true, "medium"},
		{-1, false, "low"},
		{-5, false, "low"},
	}
	for _, tc := range cases {
		got, verdict := hitVerdict(out, in, tc.netAfterHit+4, tc.netAfterHit, 5, tc.worthIt)
		if got != tc.want {
			t.Errorf("netAfterHit %v: confidence = %q, want %q", tc.netAfterHit, got, tc.want)
		}
		if verdict == "" {
			t.Errorf("netAfterHit %v: empty verdict", tc.netAfterHit)
		}
	}
}
