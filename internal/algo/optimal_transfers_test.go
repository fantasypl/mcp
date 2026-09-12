package algo

import (
	"context"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/golden"
)

// newEngineWithSquadAndHistory wires both the synthetic squad and season
// history fixtures into a stub client — optimal_transfers needs history for
// its ManagerStatus/budget derivation, unlike TransferSuggestions.
func newEngineWithSquadAndHistory(t *testing.T, fixture string) *Engine {
	t.Helper()
	squad := loadJSON[*fpl.TeamPicks](t, testdataPath("picks_squad1.json"))
	history := loadJSON[*fpl.TeamHistory](t, testdataPath("history_squad1.json"))
	c := NewStubClient(
		loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_"+fixture+".json")),
		loadJSON[[]fpl.Fixture](t, testdataPath("fixtures.json")),
	)
	c.SetTeamPicks(syntheticTeamID, 1, squad)
	c.SetHistory(syntheticTeamID, history)
	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }
	return e
}

// allow_hits isn't golden-tested here: at higher MaxChanges ceilings the
// search routinely hits optimalSquadTimeLimit before proving optimality, and
// a time-boxed search's exact squad choice at that point is
// scheduling-dependent, not a meaningful regression signal. The sweep's
// invariants (monotonic points, correct hit-cost arithmetic, exactly one
// best option, constraint satisfaction) are covered by the property-based
// tests below instead.
func TestOptimalTransfersMatchesGolden(t *testing.T) {
	bothFixtures(t, func(t *testing.T, _ *Engine, suffix string) {
		e := newEngineWithSquadAndHistory(t, suffixToFixture(suffix))

		got, err := e.OptimalTransfers(context.Background(), syntheticTeamID, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		golden.Assert(t, goldenPath("optimal_transfers_default"+suffix), got)
	})
}

func TestOptimalTransfersUnknownTeam(t *testing.T) {
	e := newEngineWithSquadAndHistory(t, "preseason")
	got, err := e.OptimalTransfers(context.Background(), 424242, nil, false)
	if err != nil {
		t.Fatalf("expected a soft error result, got a Go error: %v", err)
	}
	te, ok := got.(*TransferError)
	if !ok {
		t.Fatalf("got %T, want *TransferError", got)
	}
	if te.Error == "" {
		t.Error("expected a non-empty error message")
	}
}

func TestOptimalTransfersDefaultHasExactlyOneOption(t *testing.T) {
	e := newEngineWithSquadAndHistory(t, "midseason")
	got, err := e.OptimalTransfers(context.Background(), syntheticTeamID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	result := got.(*OptimalTransfersResult)
	if len(result.Options) != 1 {
		t.Fatalf("allow_hits=false: got %d options, want 1 (free transfers only)", len(result.Options))
	}
	if result.Options[0].HitCost != 0 {
		t.Errorf("the only option under allow_hits=false should have no hit cost, got %d", result.Options[0].HitCost)
	}
}

// One allow_hits=true call (already a multi-second, multi-Solve sweep) is
// reused across every property below rather than re-run per assertion, to
// avoid needlessly repeating the same expensive computation in every test —
// including a realistic-scale timing check through the full pipeline
// (translation, pool-cap prefilter, and up to 1+maxHitsConsidered
// sequential Solve calls), mirroring TestOptimalSquadRealisticScaleTiming's
// worst-case-elapsed approach for this tool's harder, Locked-constrained
// search.
func TestOptimalTransfersAllowHitsSweepProperties(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the multi-Solve allow_hits sweep in -short mode")
	}
	e := newEngineWithSquadAndHistory(t, "midseason")

	start := time.Now()
	got, err := e.OptimalTransfers(context.Background(), syntheticTeamID, nil, true)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	result := got.(*OptimalTransfersResult)
	if len(result.Options) != 1+maxHitsConsidered {
		t.Fatalf("allow_hits=true: got %d options, want %d", len(result.Options), 1+maxHitsConsidered)
	}

	t.Run("stays within worst-case latency", func(t *testing.T) {
		worstCase := time.Duration(1+maxHitsConsidered) * (optimalSquadTimeLimit + 2*time.Second)
		if elapsed > worstCase {
			t.Errorf("OptimalTransfers(allowHits=true) took %v, want well under %v", elapsed, worstCase)
		}
		for _, opt := range result.Options {
			t.Logf("num_transfers=%d optimal=%v net_projected_points=%.2f", opt.NumTransfers, opt.Optimal, opt.NetProjectedPoints)
		}
	})

	squad := loadJSON[*fpl.TeamPicks](t, testdataPath("picks_squad1.json"))
	lockedIDs := make(map[int]bool, len(squad.Picks))
	for _, p := range squad.Picks {
		lockedIDs[p.Element] = true
	}

	t.Run("projected points never decrease as more changes are allowed", func(t *testing.T) {
		// bnb.go's own tests already prove Solve is monotonic in
		// MaxChanges; this confirms the sweep surfaces that.
		for i := 1; i < len(result.Options); i++ {
			if result.Options[i].ProjectedPoints < result.Options[i-1].ProjectedPoints-1e-9 {
				t.Errorf("option %d projected_points (%v) is below option %d's (%v); allowing more changes should never score worse",
					i, result.Options[i].ProjectedPoints, i-1, result.Options[i-1].ProjectedPoints)
			}
		}
	})

	t.Run("exactly one option marked best", func(t *testing.T) {
		exactlyOneBest := 0
		for _, opt := range result.Options {
			if opt.Best {
				exactlyOneBest++
			}
		}
		if exactlyOneBest != 1 {
			t.Errorf("got %d options marked best, want exactly 1", exactlyOneBest)
		}
	})

	t.Run("hit cost and net points arithmetic", func(t *testing.T) {
		for _, opt := range result.Options {
			paid := max(0, opt.NumTransfers-result.FreeTransfers)
			wantHitCost := hitCost * paid
			if opt.HitCost != wantHitCost {
				t.Errorf("num_transfers=%d, free_transfers=%d: hit_cost=%d, want %d",
					opt.NumTransfers, result.FreeTransfers, opt.HitCost, wantHitCost)
			}
			wantNet := Round(opt.ProjectedPoints+float64(opt.HitCost), 2)
			if opt.NetProjectedPoints != wantNet {
				t.Errorf("net_projected_points=%v, want projected_points+hit_cost=%v", opt.NetProjectedPoints, wantNet)
			}
		}
	})

	t.Run("squad constraints", func(t *testing.T) {
		for _, opt := range result.Options {
			if len(opt.TransfersOut) != len(opt.TransfersIn) {
				t.Errorf("num_transfers=%d: %d out but %d in, squad size must stay 15",
					opt.NumTransfers, len(opt.TransfersOut), len(opt.TransfersIn))
			}
			for _, out := range opt.TransfersOut {
				if !lockedIDs[out.ID] {
					t.Errorf("transferred-out player %d was never in the original squad", out.ID)
				}
			}
			for _, in := range opt.TransfersIn {
				if lockedIDs[in.ID] {
					t.Errorf("transferred-in player %d was already in the original squad", in.ID)
				}
			}
		}
	})
}

// A brand-new manager with no recorded gameweek history should still get a
// usable budget estimate, not an error.
func TestOptimalTransfersFallsBackWithoutHistory(t *testing.T) {
	squad := loadJSON[*fpl.TeamPicks](t, testdataPath("picks_squad1.json"))
	c := NewStubClient(
		loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_midseason.json")),
		loadJSON[[]fpl.Fixture](t, testdataPath("fixtures.json")),
	)
	c.SetTeamPicks(syntheticTeamID, 1, squad)
	c.SetHistory(syntheticTeamID, &fpl.TeamHistory{})
	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }

	got, err := e.OptimalTransfers(context.Background(), syntheticTeamID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := got.(*OptimalTransfersResult)
	if !ok {
		t.Fatalf("got %T, want *OptimalTransfersResult", got)
	}
	if result.BudgetM <= 0 {
		t.Errorf("fallback budget should still be positive, got %v", result.BudgetM)
	}
}
