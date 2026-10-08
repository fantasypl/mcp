package algo

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Issue #21: the Wildcard budget is bank plus the selling prices of the
// current 15, so a player who has risen since purchase lowers it below bank
// plus market prices.
func TestWildcardBudgetUsesSellingPrices(t *testing.T) {
	ctx := context.Background()

	// No transfers: every player was held from the season start, and every
	// squad player has cost_change_start 0, so selling equals market.
	flat := newEngineWithSquadAndHistory(t, "midseason")
	stubOf(t, flat).SetTransfers(syntheticTeamID, []fpl.ManagerTransfer{})
	tcFlat, terr, err := flat.loadTransferContext(ctx, syntheticTeamID, nil)
	if err != nil || terr != nil {
		t.Fatalf("loadTransferContext: %v %v", err, terr)
	}

	risen := newEngineWithSquadAndHistory(t, "midseason")
	stubOf(t, risen).SetTransfers(syntheticTeamID, squad1Transfers)
	tcRisen, terr, err := risen.loadTransferContext(ctx, syntheticTeamID, nil)
	if err != nil || terr != nil {
		t.Fatalf("loadTransferContext: %v %v", err, terr)
	}

	// picks_squad1's bank is 0.5m and the squad is 93.0m at market prices.
	if tcFlat.budgetTenths != 5+930 {
		t.Errorf("budget with no rises = %d, want %d (bank + market)", tcFlat.budgetTenths, 5+930)
	}
	if tcRisen.budgetTenths != 5+squad1SellingTenths {
		t.Errorf("budget with risen players = %d, want %d (bank + selling)", tcRisen.budgetTenths, 5+squad1SellingTenths)
	}
	if tcRisen.budgetTenths >= tcFlat.budgetTenths {
		t.Errorf("risen players should lower the budget: %d is not below %d", tcRisen.budgetTenths, tcFlat.budgetTenths)
	}
}

// One full Wildcard search, reused across the properties below.
func TestOptimalWildcard(t *testing.T) {
	e := newEngineWithSquadAndHistory(t, "midseason")
	stubOf(t, e).SetTransfers(syntheticTeamID, squad1Transfers)

	start := time.Now()
	got, err := e.OptimalWildcard(context.Background(), syntheticTeamID, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := got.(*WildcardResult)
	if !ok {
		t.Fatalf("got %T, want *WildcardResult", got)
	}
	t.Logf("elapsed=%v optimal=%v transfers=%d gain=%.2f", elapsed, res.Optimal, res.NumTransfers, res.ProjectedGain)

	t.Run("stays within one search time limit", func(t *testing.T) {
		if worst := optimalSquadTimeLimit + 4*time.Second; elapsed > worst {
			t.Errorf("took %v, want under %v", elapsed, worst)
		}
		if res.Partial == res.Optimal {
			t.Errorf("partial = %v and optimal = %v, want opposites", res.Partial, res.Optimal)
		}
		if (res.PartialNote != "") != res.Partial {
			t.Errorf("partial_note = %q, want it set exactly when partial", res.PartialNote)
		}
	})

	t.Run("ignores hit cost", func(t *testing.T) {
		// The fixture's manager has 1 free transfer. A Wildcard search is
		// not held to it, and the result carries no hit cost at all.
		if res.NumTransfers <= 1 {
			t.Errorf("num_transfers = %d, want more than the 1 free transfer a normal week allows", res.NumTransfers)
		}
		if !strings.Contains(res.WildcardNote, "no hit cost") {
			t.Errorf("wildcard_note = %q, want it to say there is no hit cost", res.WildcardNote)
		}
	})

	t.Run("respects the selling-price budget", func(t *testing.T) {
		if want := float64(5+squad1SellingTenths) / 10; res.BudgetM != want {
			t.Errorf("budget_m = %v, want %v", res.BudgetM, want)
		}
		if res.BudgetUsedM > res.BudgetM {
			t.Errorf("budget_used_m %v is over budget_m %v", res.BudgetUsedM, res.BudgetM)
		}
		if left := math.Round((res.BudgetM-res.BudgetUsedM)*10) / 10; math.Abs(res.BudgetLeftM-left) > 1e-9 {
			t.Errorf("budget_left_m = %v, want %v", res.BudgetLeftM, left)
		}
		if !strings.Contains(res.BudgetNote, "public transfer history") {
			t.Errorf("budget_note = %q, want it to say selling prices come from the public transfer history", res.BudgetNote)
		}
	})

	t.Run("gain over the current squad", func(t *testing.T) {
		if want := Round(res.ProjectedPoints-res.CurrentProjectedPoints, 2); math.Abs(res.ProjectedGain-want) > 0.011 {
			t.Errorf("projected_gain = %v, want projected_points - current_projected_points = %v", res.ProjectedGain, want)
		}
		if res.ProjectedGain < 0 {
			t.Errorf("projected_gain = %v; keeping the current squad is always an option, so it can't be negative", res.ProjectedGain)
		}
	})

	t.Run("squad shape and lineup", func(t *testing.T) {
		if len(res.Squad) != 15 {
			t.Fatalf("squad has %d players, want 15", len(res.Squad))
		}
		if len(res.TransfersIn) != len(res.TransfersOut) || len(res.TransfersIn) != res.NumTransfers {
			t.Errorf("%d in, %d out, num_transfers %d: want all equal", len(res.TransfersIn), len(res.TransfersOut), res.NumTransfers)
		}
		if len(res.StartingXI) != 11 || len(res.BenchOrder) != 4 || res.Formation == "" {
			t.Errorf("starting_xi %d, bench_order %d, formation %q: want 11, 4 and a formation", len(res.StartingXI), len(res.BenchOrder), res.Formation)
		}
		if res.RecommendedCaptain == nil {
			t.Error("recommended_captain is nil")
		}
	})
}

// The gain compares two squads under one objective: the best XI plus the
// bench at benchWeight.
func TestSquadObjectiveMatchesSolve(t *testing.T) {
	c := SquadConstraints{BudgetTenths: 1000, PositionQuota: fplQuota, MaxPerClub: 3, MaxChanges: -1, Lineup: &fplLineup}
	var squad []Candidate
	id := 1
	for pos := 1; pos <= numPositions; pos++ {
		for i := 0; i < fplQuota[pos]; i++ {
			squad = append(squad, Candidate{ID: id, Position: pos, PriceTenths: 50, Club: id, Value: float64(10 + id)})
			id++
		}
	}
	r, err := Solve(squad, c)
	if err != nil {
		t.Fatal(err)
	}
	if got := squadObjective(squad, c); math.Abs(got-r.Value) > 1e-9 {
		t.Errorf("squadObjective = %v, Solve's value for the same 15 = %v", got, r.Value)
	}
	// A plain sum would count the bench in full.
	if got := squadObjective(squad, c); got >= sumValue(squad) {
		t.Errorf("squadObjective = %v, want below the plain sum %v (bench discounted)", got, sumValue(squad))
	}
}

func TestWildcardAvailability(t *testing.T) {
	active := "wildcard"
	cases := []struct {
		name string
		m    fpl.ManagerStatus
		want bool
	}{
		{"remaining", fpl.ManagerStatus{ChipsRemaining: []string{"wildcard", "3xc"}}, true},
		{"active this gameweek", fpl.ManagerStatus{ChipActiveThisGW: &active}, true},
		{"used", fpl.ManagerStatus{ChipsRemaining: []string{"3xc"}}, false},
	}
	for _, tc := range cases {
		if got, note := wildcardAvailability(&tc.m); got != tc.want || note == "" {
			t.Errorf("%s: available = %v (note %q), want %v", tc.name, got, note, tc.want)
		}
	}
}
