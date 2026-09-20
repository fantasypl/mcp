package algo

import (
	"context"
	"strings"
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

	t.Run("every option carries confidence, exactly one is safest, and the note says what best means", func(t *testing.T) {
		safest := 0
		for _, opt := range result.Options {
			switch opt.Confidence {
			case ConfidenceHigh, ConfidenceMedium, ConfidenceLow:
			default:
				t.Errorf("num_transfers=%d: Confidence = %q, want high, medium or low", opt.NumTransfers, opt.Confidence)
			}
			if opt.Safest {
				safest++
			}
		}
		if safest != 1 {
			t.Errorf("got %d options marked safest, want exactly 1", safest)
		}
		if !strings.Contains(result.BestNote, "highest") || !strings.Contains(result.BestNote, "not") {
			t.Errorf("BestNote = %q, want it to say best means highest projection, not lowest risk", result.BestNote)
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

// Issue #5: an option's confidence is that of its weakest incoming player, judged
// on how much playing time backs the projection and whether goal involvements
// run well ahead of the underlying chances.
func TestAssessIncomingPlayer(t *testing.T) {
	cases := []struct {
		name      string
		player    fpl.Player
		wantLevel string
		wantNote  bool
	}{
		{"established starter", fpl.Player{Minutes: 2700, GoalsScored: 10, Assists: 5, ExpectedGoals: 9.5, ExpectedAssists: 4.5}, ConfidenceHigh, false},
		{"some minutes", fpl.Player{Minutes: 600}, ConfidenceMedium, true},
		{"the issue's low-minutes forward", fpl.Player{Minutes: 105}, ConfidenceLow, true},
		{"minutes boundary: 300 is not low", fpl.Player{Minutes: 300}, ConfidenceMedium, true},
		{"minutes boundary: 900 is not medium", fpl.Player{Minutes: 900}, ConfidenceHigh, false},
		// 1500 minutes would be high, but 12 involvements from 6.0 xGI is a hot streak.
		{"overperforming its chances", fpl.Player{Minutes: 1500, GoalsScored: 8, Assists: 4, ExpectedGoals: 4.0, ExpectedAssists: 2.0}, ConfidenceMedium, true},
		{"overperforming and few minutes", fpl.Player{Minutes: 400, GoalsScored: 6, Assists: 2, ExpectedGoals: 2.0, ExpectedAssists: 1.0}, ConfidenceLow, true},
		{"small overperformance is noise", fpl.Player{Minutes: 1500, GoalsScored: 4, Assists: 2, ExpectedGoals: 3.0, ExpectedAssists: 1.5}, ConfidenceHigh, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.player.WebName = "Test"
			level, note := assessIncomingPlayer(&tc.player)
			if level != tc.wantLevel {
				t.Errorf("level = %q, want %q (note %q)", level, tc.wantLevel, note)
			}
			if (note != "") != tc.wantNote {
				t.Errorf("note = %q, wantNote = %v", note, tc.wantNote)
			}
		})
	}
}

func TestSummarizeOptionConfidence(t *testing.T) {
	solid := &fpl.Player{WebName: "Solid", Minutes: 2000}
	thin := &fpl.Player{WebName: "Thin", Minutes: 105}

	level, notes := summarizeOptionConfidence(nil)
	if level != ConfidenceHigh || len(notes) != 0 {
		t.Errorf("no transfers: %q %v, want high with no notes", level, notes)
	}

	level, notes = summarizeOptionConfidence([]*fpl.Player{solid, thin})
	if level != ConfidenceLow {
		t.Errorf("level = %q, want low: one thin leg drags the option down", level)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "Thin") {
		t.Errorf("notes = %v, want a single note naming Thin", notes)
	}
}

func TestMarkSafest(t *testing.T) {
	opts := []TransferPlanOption{
		{NumTransfers: 1, NetProjectedPoints: 430, Confidence: ConfidenceHigh},
		{NumTransfers: 2, NetProjectedPoints: 465, Confidence: ConfidenceHigh},
		{NumTransfers: 3, NetProjectedPoints: 486, Confidence: ConfidenceLow, Best: true},
	}
	markSafest(opts)
	for i, want := range []bool{false, true, false} {
		if opts[i].Safest != want {
			t.Errorf("option %d Safest = %v, want %v", i, opts[i].Safest, want)
		}
	}
	if !opts[2].Best || opts[2].Safest {
		t.Error("the issue's case: best (486, low confidence) and safest must be different options")
	}
}
