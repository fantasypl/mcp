package algo

import (
	"math"
	"math/rand"
	"slices"
	"testing"
	"time"
)

// The lineup objective (#31) is cross-checked against bruteForceSolve,
// which scores squads with its own lineup enumerator (bruteLineupValue
// below) rather than anything in bnb.go.

// reducedLineup suits the reduced 2/3/3/2 quota the older brute-force tests
// use: one keeper and five outfielders start, at least one of each.
var reducedLineup = LineupRules{Size: 6, Min: [5]int{0, 1, 1, 1, 1}, Max: [5]int{0, 1, 3, 3, 2}, BenchWeight: 0.15}

func TestBnBLineupMatchesBruteForce(t *testing.T) {
	// The brute-force checks are CPU-bound and share no state, so they run
	// in parallel. Parallel tests start only after the sequential ones end,
	// so they never compete with the timing tests for CPU.
	t.Parallel()
	cases := []struct {
		name   string
		seed   int64
		split  [5]int
		quota  [5]int
		lineup LineupRules
		trials int
		clubs  int
	}{
		{"reduced quota", 31, [5]int{0, 6, 8, 8, 6}, [5]int{0, 2, 3, 3, 2}, reducedLineup, 20, 6},
		// One deep block, like TestBnBMatchesBruteForceDeepBlock.
		{"deep block", 32, [5]int{0, 3, 15, 4, 4}, [5]int{0, 2, 3, 2, 2},
			LineupRules{Size: 5, Min: [5]int{0, 1, 1, 1, 1}, Max: [5]int{0, 1, 3, 2, 2}, BenchWeight: 0.1}, 10, 6},
		// The real 2/5/5/3 quota and FPL formations, on small pools.
		{"FPL quota and formations", 33, [5]int{0, 3, 7, 7, 5}, fplQuota, fplLineup, 10, 10},
		// Bench weight 0 and 1 are the edges: bench ignored, and the plain sum.
		{"bench weight 0", 34, [5]int{0, 6, 8, 8, 6}, [5]int{0, 2, 3, 3, 2}, withBenchWeight(reducedLineup, 0), 10, 6},
		{"bench weight 1", 35, [5]int{0, 6, 8, 8, 6}, [5]int{0, 2, 3, 3, 2}, withBenchWeight(reducedLineup, 1), 10, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewSource(tc.seed))
			for trial := 0; trial < tc.trials; trial++ {
				candidates := randomCandidates(rng, tc.split, tc.clubs)
				// A few negative values (FPL form can go negative) check the
				// clamped bounds stay safe.
				for i := range candidates {
					if rng.Intn(8) == 0 {
						candidates[i].Value = -rng.Float64() * 2
					}
				}
				proof, err := Solve(candidates, SquadConstraints{BudgetTenths: 1 << 30, PositionQuota: tc.quota, MaxPerClub: 3, MaxChanges: -1})
				if err != nil {
					t.Fatalf("trial %d: proof-of-feasibility solve failed: %v", trial, err)
				}
				minFeasibleCost := 0
				for _, c := range proof.Squad {
					minFeasibleCost += c.PriceTenths
				}
				lineup := tc.lineup
				constraints := SquadConstraints{
					BudgetTenths:  minFeasibleCost + rng.Intn(200),
					PositionQuota: tc.quota,
					MaxPerClub:    3,
					MaxChanges:    -1,
					Lineup:        &lineup,
				}

				got, err := Solve(candidates, constraints)
				if err != nil {
					t.Fatalf("trial %d: Solve: %v", trial, err)
				}
				assertValidSquad(t, got.Squad, constraints)
				if !got.Optimal {
					t.Fatalf("trial %d: Optimal = false with no time limit", trial)
				}
				// The returned squad carries true values and is scored as
				// the brute force would score it.
				if v := bruteLineupValue(got.Squad, constraints); math.Abs(v-got.Value) > 1e-6 {
					t.Fatalf("trial %d: reported value %v, but the squad scores %v", trial, got.Value, v)
				}

				want := bruteForceSolve(candidates, constraints)
				if math.IsInf(want.Value, -1) {
					t.Fatalf("trial %d: brute force found no feasible squad, but Solve returned one (value %v)", trial, got.Value)
				}
				if diff := got.Value - want.Value; math.Abs(diff) > 1e-6 {
					t.Fatalf("trial %d: Solve value = %v, brute force = %v (budget %d)", trial, got.Value, want.Value, constraints.BudgetTenths)
				}
			}
		})
	}
}

// A lineup objective changes which squad wins, not just its score: given
// one strong expensive player and cheap filler, it should prefer spending
// on starters over a bench that barely counts.
func TestLineupObjectiveSpendsOnStarters(t *testing.T) {
	var cands []Candidate
	id := 1
	add := func(pos, price int, value float64) {
		cands = append(cands, Candidate{ID: id, Position: pos, PriceTenths: price, Club: id, Value: value})
		id++
	}
	// Quota 1 GKP, 2 DEF; one DEF starts. Budget 100.
	add(1, 10, 1)
	// Option A: two mid-priced defenders worth 6 each (cost 90).
	add(2, 45, 6)
	add(2, 45, 6)
	// Option B: one star worth 10 plus a 4.0m bench defender worth 0 (cost 90).
	add(2, 50, 10)
	add(2, 40, 0)
	lineup := LineupRules{Size: 2, Min: [5]int{0, 1, 1, 0, 0}, Max: [5]int{0, 1, 1, 0, 0}, BenchWeight: 0.1}
	c := SquadConstraints{BudgetTenths: 100, PositionQuota: [5]int{0, 1, 2, 0, 0}, MaxPerClub: 3, MaxChanges: -1}

	plain, err := Solve(cands, c)
	if err != nil {
		t.Fatal(err)
	}
	c.Lineup = &lineup
	weighted, err := Solve(cands, c)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Value != 13 {
		t.Errorf("plain sum = %v, want 13 (the two 6-point defenders)", plain.Value)
	}
	if math.Abs(weighted.Value-11) > 1e-9 {
		t.Errorf("lineup value = %v, want 11 (the star starts, the 0-point defender sits)", weighted.Value)
	}
}

func TestLineupRulesValidation(t *testing.T) {
	base := SquadConstraints{BudgetTenths: 1000, PositionQuota: fplQuota, MaxPerClub: 3, MaxChanges: -1}
	cands := randomCandidates(rand.New(rand.NewSource(1)), [5]int{0, 3, 7, 7, 5}, 6)
	for name, l := range map[string]LineupRules{
		"bench weight above 1":  withBenchWeight(fplLineup, 1.5),
		"negative bench weight": withBenchWeight(fplLineup, -0.1),
		"min above quota":       {Size: 11, Min: [5]int{0, 3, 3, 2, 1}, Max: [5]int{0, 3, 5, 5, 3}},
		"size unreachable":      {Size: 16, Min: fplLineup.Min, Max: fplLineup.Max},
	} {
		c := base
		c.Lineup = &l
		if _, err := Solve(cands, c); err == nil {
			t.Errorf("%s: want an error, got nil", name)
		}
	}
}

func TestEnumerateBenchesFPL(t *testing.T) {
	c := SquadConstraints{PositionQuota: fplQuota, Lineup: &fplLineup}
	got := enumerateBenches(c)
	// 3-4-3, 3-5-2, 4-3-3, 4-4-2, 4-5-1, 5-2-3, 5-3-2, 5-4-1.
	if len(got) != 8 {
		t.Fatalf("got %d formations, want FPL's 8: %v", len(got), got)
	}
	for _, b := range got {
		if b[1] != 1 || b[2]+b[3]+b[4] != 3 {
			t.Errorf("bench %v: want 1 keeper and 3 outfielders benched", b)
		}
	}
}

// The lineup objective at real FPL scale, logged next to the plain sum so a
// change in tractability is visible. Like solveAtRealisticScale, this
// asserts the time limit holds rather than that every seed proves optimal.
func TestSolveLineupAtRealisticFPLScale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping realistic-scale lineup solve in -short mode")
	}
	const timeLimit = 8 * time.Second
	for _, clustered := range []bool{false, true} {
		for _, seed := range []int64{1, 2, 3, 4, 5} {
			candidates := realisticFPLCandidates(rand.New(rand.NewSource(seed)), clustered)
			c := SquadConstraints{
				BudgetTenths: realisticFPLBudgetTenths, PositionQuota: fplQuota, MaxPerClub: 3, MaxChanges: -1,
				TimeLimit: timeLimit, Lineup: &fplLineup,
			}
			start := time.Now()
			got, nodes, err := solveDebug(candidates, c)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			assertValidSquad(t, got.Squad, c)
			t.Logf("seed %d clustered=%v: %v, %d nodes, optimal=%v, value=%.2f", seed, clustered, elapsed, nodes, got.Optimal, got.Value)
			if elapsed > timeLimit+2*time.Second {
				t.Errorf("seed %d clustered=%v: took %v, want under %v", seed, clustered, elapsed, timeLimit+2*time.Second)
			}
		}
	}
}

func withBenchWeight(l LineupRules, w float64) LineupRules {
	l.BenchWeight = w
	return l
}

// bruteLineupValue scores a complete squad independently of bnb.go: it
// tries every count of starters per position allowed by the rules, starts
// that many of each position's best players, and keeps the best total.
func bruteLineupValue(squad []Candidate, c SquadConstraints) float64 {
	// Fixed-size arrays keep this allocation-free, since the brute force
	// calls it once per feasible squad. No test squad exceeds 15 players.
	var vals [5][15]float64
	var count [5]int
	for _, cnd := range squad {
		vals[cnd.Position][count[cnd.Position]] = cnd.Value
		count[cnd.Position]++
	}
	var byPos [5][]float64
	for pos := range byPos {
		byPos[pos] = vals[pos][:count[pos]]
		slices.Sort(byPos[pos])
		slices.Reverse(byPos[pos])
	}
	return bruteBestLineup(c.Lineup, &byPos, 1, 0, 0)
}

// bruteBestLineup tries every allowed starter count for each position from
// pos onward and returns the best total, or -Inf if none fills the lineup.
func bruteBestLineup(l *LineupRules, byPos *[5][]float64, pos, started int, total float64) float64 {
	if pos == 5 {
		if started == l.Size {
			return total
		}
		return math.Inf(-1)
	}
	best := math.Inf(-1)
	vals := byPos[pos]
	for n := l.Min[pos]; n <= l.Max[pos] && n <= len(vals); n++ {
		sub := 0.0
		for i, v := range vals {
			if i < n {
				sub += v
			} else {
				sub += l.BenchWeight * v
			}
		}
		best = max(best, bruteBestLineup(l, byPos, pos+1, started+n, total+sub))
	}
	return best
}
