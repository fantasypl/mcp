package algo

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
)

// squadOf builds a 15-man squad: 2 GKP, 5 DEF, 5 MID, 3 FWD, with the given
// projected points in that order.
func squadOf(gkp [2]float64, def [5]float64, mid [5]float64, fwd [3]float64) []OptimalSquadSlot {
	var squad []OptimalSquadSlot
	id := 1
	add := func(pos string, points ...float64) {
		for _, p := range points {
			squad = append(squad, OptimalSquadSlot{ID: id, Name: fmt.Sprintf("%s%d", pos, id), Position: pos, ProjectedPoints: p})
			id++
		}
	}
	add("GKP", gkp[:]...)
	add("DEF", def[:]...)
	add("MID", mid[:]...)
	add("FWD", fwd[:]...)
	return squad
}

func TestChooseLineupFormation(t *testing.T) {
	cases := []struct {
		name          string
		squad         []OptimalSquadSlot
		wantFormation string
	}{
		{
			"strong forwards push to a front three",
			squadOf([2]float64{4, 1}, [5]float64{5, 5, 5, 1, 1}, [5]float64{6, 6, 6, 6, 1}, [3]float64{9, 9, 9}),
			"3-4-3",
		},
		{
			"strong defenders push to a back five",
			squadOf([2]float64{4, 1}, [5]float64{9, 9, 9, 9, 9}, [5]float64{5, 5, 1, 1, 1}, [3]float64{5, 1, 1}),
			"5-2-3",
		},
		{
			// 3-5-2 and 4-5-1 both score 66 here; the tie goes to the first
			// formation enumerated, the one with fewer defenders.
			"strong midfield pushes to five in midfield, ties to fewer defenders",
			squadOf([2]float64{4, 1}, [5]float64{5, 5, 5, 1, 1}, [5]float64{9, 9, 9, 9, 9}, [3]float64{5, 1, 1}),
			"3-5-2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chooseLineup(tc.squad); got.Formation != tc.wantFormation {
				t.Errorf("formation = %s, want %s", got.Formation, tc.wantFormation)
			}
		})
	}
}

func TestChooseLineupGoalkeeperAndBench(t *testing.T) {
	squad := squadOf([2]float64{2, 6}, [5]float64{5, 5, 5, 4, 1}, [5]float64{6, 6, 6, 6, 3}, [3]float64{7, 7, 2})
	got := chooseLineup(squad)

	// The better keeper (ID 2, 6 points) starts; the other opens the bench.
	if got.XI[0] != 2 {
		t.Errorf("starting goalkeeper = %d, want 2", got.XI[0])
	}
	if len(got.XI) != 11 || len(got.Bench) != 4 {
		t.Fatalf("got %d starters and %d on the bench, want 11 and 4", len(got.XI), len(got.Bench))
	}
	if got.Bench[0] != 1 {
		t.Errorf("first bench slot = %d, want the spare goalkeeper (1)", got.Bench[0])
	}

	// Outfield bench in descending projected points.
	byID := map[int]float64{}
	for _, s := range squad {
		byID[s.ID] = s.ProjectedPoints
	}
	for i := 2; i < len(got.Bench); i++ {
		if byID[got.Bench[i]] > byID[got.Bench[i-1]] {
			t.Errorf("bench not sorted by projected points: %v", got.Bench)
		}
	}
}

// Brute force: try every 11-of-15 subset, keep the valid ones, and check the
// chosen lineup scores exactly the best. Random squads exercise the
// position-cap edges that hand-written cases miss.
func TestChooseLineupMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 200; trial++ {
		var gkp [2]float64
		var def, mid [5]float64
		var fwd [3]float64
		for i := range gkp {
			gkp[i] = rng.Float64() * 10
		}
		for i := range def {
			def[i], mid[i] = rng.Float64()*10, rng.Float64()*10
		}
		for i := range fwd {
			fwd[i] = rng.Float64() * 10
		}
		squad := squadOf(gkp, def, mid, fwd)

		want := bruteForceBestXI(squad)
		got := chooseLineup(squad)

		byID := map[int]OptimalSquadSlot{}
		for _, s := range squad {
			byID[s.ID] = s
		}
		var total float64
		counts := map[string]int{}
		for _, id := range got.XI {
			total += byID[id].ProjectedPoints
			counts[byID[id].Position]++
		}
		if len(got.XI) != 11 || counts["GKP"] != 1 || counts["DEF"] < 3 || counts["DEF"] > 5 ||
			counts["MID"] < 2 || counts["MID"] > 5 || counts["FWD"] < 1 || counts["FWD"] > 3 {
			t.Fatalf("trial %d: invalid lineup %v (%v)", trial, got.XI, counts)
		}
		if total < want-1e-9 {
			t.Fatalf("trial %d: lineup scores %.4f, brute force finds %.4f", trial, total, want)
		}
		wantFormation := fmt.Sprintf("%d-%d-%d", counts["DEF"], counts["MID"], counts["FWD"])
		if got.Formation != wantFormation {
			t.Fatalf("trial %d: formation %q does not describe the XI (%s)", trial, got.Formation, wantFormation)
		}
	}
}

func bruteForceBestXI(squad []OptimalSquadSlot) float64 {
	best := -1.0
	n := len(squad)
	for mask := 0; mask < 1<<n; mask++ {
		count := 0
		for m := mask; m != 0; m &= m - 1 {
			count++
		}
		if count != 11 {
			continue
		}
		pos := map[string]int{}
		total := 0.0
		for i := 0; i < n; i++ {
			if mask&(1<<i) != 0 {
				pos[squad[i].Position]++
				total += squad[i].ProjectedPoints
			}
		}
		if pos["GKP"] == 1 && pos["DEF"] >= 3 && pos["DEF"] <= 5 && pos["MID"] >= 2 && pos["MID"] <= 5 && pos["FWD"] >= 1 && pos["FWD"] <= 3 {
			best = max(best, total)
		}
	}
	return best
}

// End to end: optimal_squad returns a lineup and captain alongside the 15.
func TestOptimalSquadIncludesLineupAndCaptain(t *testing.T) {
	e := newEngineForOptimalSquad(t, "midseason")
	got, err := e.OptimalSquad(context.Background(), 1000, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.StartingXI) != 11 {
		t.Fatalf("starting_xi has %d players, want 11", len(got.StartingXI))
	}
	if len(got.BenchOrder) != 4 {
		t.Fatalf("bench_order has %d players, want 4", len(got.BenchOrder))
	}
	inSquad := map[int]OptimalSquadSlot{}
	for _, s := range got.Squad {
		inSquad[s.ID] = s
	}
	seen := map[int]bool{}
	counts := map[string]int{}
	for _, id := range append(append([]int{}, got.StartingXI...), got.BenchOrder...) {
		if _, ok := inSquad[id]; !ok {
			t.Errorf("player %d is in the lineup but not the squad", id)
		}
		if seen[id] {
			t.Errorf("player %d appears twice across starting_xi and bench_order", id)
		}
		seen[id] = true
	}
	for _, id := range got.StartingXI {
		counts[inSquad[id].Position]++
	}
	if want := fmt.Sprintf("%d-%d-%d", counts["DEF"], counts["MID"], counts["FWD"]); got.Formation != want {
		t.Errorf("formation = %q, want %q", got.Formation, want)
	}

	if got.RecommendedCaptain == nil || got.RecommendedViceCaptain == nil {
		t.Fatal("expected a recommended captain and vice-captain")
	}
	starters := map[int]bool{}
	for _, id := range got.StartingXI {
		starters[id] = true
	}
	for _, c := range []*LineupCaptain{got.RecommendedCaptain, got.RecommendedViceCaptain} {
		if !starters[c.ID] {
			t.Errorf("%s (%d) is captain-listed but not in the starting XI", c.Name, c.ID)
		}
		if c.Reason == "" {
			t.Errorf("%s has no reason", c.Name)
		}
	}
	if got.RecommendedCaptain.ID == got.RecommendedViceCaptain.ID {
		t.Error("captain and vice-captain must differ")
	}
	if got.RecommendedCaptain.Score < got.RecommendedViceCaptain.Score {
		t.Errorf("captain score %.2f is below vice-captain's %.2f", got.RecommendedCaptain.Score, got.RecommendedViceCaptain.Score)
	}
}
