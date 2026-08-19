package algo

import (
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"
)

// TestBnBMatchesBruteForce checks Solve against a deliberately separate,
// naively-written brute-force enumerator (bruteForceSolve below, sharing no
// code with bnb.go) across randomized reduced instances — two independent
// implementations agreeing rules out a shared logic error being mistaken
// for correctness.
//
// The instance size here (6/8/8/6 candidates per position, quota 2/3/3/2)
// is smaller than optimal_squad's real 2/5/5/3 — a full 2/5/5/3-quota
// brute-force enumeration at a ~40-candidate scale is on the order of a
// billion combinations, infeasible for a unit test. This size keeps full
// enumeration sub-second while still exercising every constraint (budget,
// per-position quota, per-club cap) the same way. Solve's behavior at
// full FPL scale (~150 candidates post-prune, real 2/5/5/3 quota) is
// checked separately for speed in TestSolveTimingAtRealisticScale.
func TestBnBMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	split := [5]int{0, 6, 8, 8, 6}
	quota := [5]int{0, 2, 3, 3, 2}

	for trial := 0; trial < 20; trial++ {
		candidates := randomCandidates(rng, split, 6)

		// Guarantee feasibility deterministically rather than hoping a
		// random budget happens to clear the club-cap-constrained minimum:
		// solve once with an effectively unlimited budget to get a known
		// feasible squad, then test at that squad's own cost plus jitter —
		// any budget at or above a known feasible squad's cost is itself
		// feasible, by definition.
		proof, err := Solve(candidates, SquadConstraints{BudgetTenths: 1 << 30, PositionQuota: quota, MaxPerClub: 3, MaxChanges: -1})
		if err != nil {
			t.Fatalf("trial %d: proof-of-feasibility solve failed: %v", trial, err)
		}
		minFeasibleCost := 0
		for _, c := range proof.Squad {
			minFeasibleCost += c.PriceTenths
		}

		constraints := SquadConstraints{
			BudgetTenths:  minFeasibleCost + rng.Intn(200),
			PositionQuota: quota,
			MaxPerClub:    3,
			MaxChanges:    -1,
		}

		got, err := Solve(candidates, constraints)
		if err != nil {
			t.Fatalf("trial %d: Solve: %v", trial, err)
		}
		assertValidSquad(t, got.Squad, constraints)

		want := bruteForceSolve(candidates, constraints)
		if math.IsInf(want.Value, -1) {
			t.Fatalf("trial %d: brute force found no feasible squad, but Solve returned one (value %v)", trial, got.Value)
		}
		if diff := got.Value - want.Value; diff > 1e-6 || diff < -1e-6 {
			t.Fatalf("trial %d: Solve value = %v, brute force = %v (budget %d)", trial, got.Value, want.Value, constraints.BudgetTenths)
		}
	}
}

// TestBnBRespectsLockedAndMaxChanges exercises the optimal_transfers-only
// constraint dimension: a locked "current squad" plus a cap on how many
// non-locked candidates may be selected.
func TestBnBRespectsLockedAndMaxChanges(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	split := [5]int{0, 6, 8, 8, 6}
	quota := [5]int{0, 2, 3, 3, 2}
	candidates := randomCandidates(rng, split, 6)

	// Build a genuinely valid "current squad" via Solve itself (unlimited
	// budget, unlimited changes) rather than an ad hoc greedy pick — a
	// hand-rolled pick risks violating the club cap by construction, which
	// would make the MaxChanges=0 assertion below meaningless.
	seed, err := Solve(candidates, SquadConstraints{BudgetTenths: 1 << 30, PositionQuota: quota, MaxPerClub: 3, MaxChanges: -1})
	if err != nil {
		t.Fatalf("building a seed squad: %v", err)
	}
	var lockedIDs []int
	spent := 0
	for _, c := range seed.Squad {
		lockedIDs = append(lockedIDs, c.ID)
		spent += c.PriceTenths
	}
	budget := spent + 50 // a little headroom over the locked squad's own cost

	// MaxChanges=0: the only feasible squad is the locked one itself.
	zeroChange := SquadConstraints{BudgetTenths: budget, PositionQuota: quota, MaxPerClub: 3, Locked: lockedIDs, MaxChanges: 0}
	got, err := Solve(candidates, zeroChange)
	if err != nil {
		t.Fatalf("MaxChanges=0: Solve: %v", err)
	}
	assertValidSquad(t, got.Squad, zeroChange)
	gotIDs := map[int]bool{}
	for _, c := range got.Squad {
		gotIDs[c.ID] = true
	}
	for _, id := range lockedIDs {
		if !gotIDs[id] {
			t.Errorf("MaxChanges=0: locked candidate %d missing from result", id)
		}
	}
	for _, c := range got.Squad {
		if !toSet(lockedIDs)[c.ID] {
			t.Errorf("MaxChanges=0: result includes non-locked candidate %d", c.ID)
		}
	}

	// MaxChanges=-1 (unlimited) must never score worse than MaxChanges=0,
	// since 0 is always a feasible (if not optimal) choice under unlimited.
	unlimited := SquadConstraints{BudgetTenths: budget, PositionQuota: quota, MaxPerClub: 3, Locked: lockedIDs, MaxChanges: -1}
	gotUnlimited, err := Solve(candidates, unlimited)
	if err != nil {
		t.Fatalf("MaxChanges=-1: Solve: %v", err)
	}
	assertValidSquad(t, gotUnlimited.Squad, unlimited)
	if gotUnlimited.Value < got.Value-1e-9 {
		t.Errorf("MaxChanges=-1 value %v is worse than MaxChanges=0 value %v", gotUnlimited.Value, got.Value)
	}
}

// TestSolveTimingAtCurrentScale isn't a correctness test — it's a sanity
// check that Solve stays fast at a scale it's actually verified to handle
// today.
//
// RESOLVED: this test originally documented a KNOWN GAP — at the real FPL
// quota (2/5/5/3) and a realistic post-dominance-prune candidate count
// (~150-190), Solve exhibited exponential blowup (19 candidates ~110ms,
// 23 ~1s, 27 ~6s, 31+ regularly exceeds 8s). Root cause: bound()'s
// per-position DP table was built once globally and never narrowed as the
// search decided candidates within a position's block, so it kept
// treating already-excluded candidates as available — loosest exactly at
// the large-quota (MID/DEF) positions where the tree is biggest. Fixed by
// buildPositionSuffixDP (see bnb.go), which gives bound() a node-scoped
// table restricted to genuinely still-available candidates instead. See
// TestSolveAtRealisticFPLScale and TestSolveAtRealisticFPLScaleClustered
// for the real-scale regression coverage that replaces this gap note.
//
// This test still uses a small scale deliberately, purely as a fast
// smoke check — not a stand-in for the real-scale tests below.
func TestSolveTimingAtCurrentScale(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	candidates := randomCandidates(rng, [5]int{0, 4, 7, 7, 5}, 20)
	quota := [5]int{0, 2, 5, 5, 3}

	// A fixed budget risks flaking on infeasibility depending on the random
	// draw (real per-player prices vary, and 15 total picks can exceed a
	// guessed budget) — derive a guaranteed-feasible one from an unlimited
	// solve first, same as the correctness tests' pattern. Budget doesn't
	// bind this unconstrained solve, so it stays fast even unlimited.
	unconstrained, err := Solve(candidates, SquadConstraints{BudgetTenths: 1 << 30, PositionQuota: quota, MaxPerClub: 3, MaxChanges: -1})
	if err != nil {
		t.Fatalf("unconstrained solve: %v", err)
	}
	feasibleCost := 0
	for _, c := range unconstrained.Squad {
		feasibleCost += c.PriceTenths
	}
	constraints := SquadConstraints{
		BudgetTenths:  feasibleCost,
		PositionQuota: quota,
		MaxPerClub:    3,
		MaxChanges:    -1,
	}
	start := time.Now()
	got, err := Solve(candidates, constraints)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	assertValidSquad(t, got.Squad, constraints)
	t.Logf("Solve over %d candidates took %v", len(candidates), elapsed)
	if elapsed > 10*time.Second {
		t.Errorf("Solve took %v at this scale, want well under 10s", elapsed)
	}
}

// TestBnBMatchesBruteForceDeepBlock is TestBnBMatchesBruteForce's
// correctness cross-check, but at a shape specifically chosen to stress
// the bug buildPositionSuffixDP fixes: one position (DEF) has far more
// candidates than its quota, forcing many sequential in/exclude decisions
// within that single block before the quota fills — exactly where the old
// global per-position table stayed loosest longest. Other positions are
// kept small so full brute-force enumeration still finishes instantly.
func TestBnBMatchesBruteForceDeepBlock(t *testing.T) {
	rng := rand.New(rand.NewSource(2024))
	split := [5]int{0, 3, 15, 3, 3}
	quota := [5]int{0, 1, 3, 1, 1}

	for trial := 0; trial < 10; trial++ {
		candidates := randomCandidates(rng, split, 6)

		proof, err := Solve(candidates, SquadConstraints{BudgetTenths: 1 << 30, PositionQuota: quota, MaxPerClub: 3, MaxChanges: -1})
		if err != nil {
			t.Fatalf("trial %d: proof-of-feasibility solve failed: %v", trial, err)
		}
		minFeasibleCost := 0
		for _, c := range proof.Squad {
			minFeasibleCost += c.PriceTenths
		}

		constraints := SquadConstraints{
			BudgetTenths:  minFeasibleCost + rng.Intn(200),
			PositionQuota: quota,
			MaxPerClub:    3,
			MaxChanges:    -1,
		}

		got, err := Solve(candidates, constraints)
		if err != nil {
			t.Fatalf("trial %d: Solve: %v", trial, err)
		}
		assertValidSquad(t, got.Squad, constraints)

		want := bruteForceSolve(candidates, constraints)
		if math.IsInf(want.Value, -1) {
			t.Fatalf("trial %d: brute force found no feasible squad, but Solve returned one (value %v)", trial, got.Value)
		}
		if diff := got.Value - want.Value; diff > 1e-6 || diff < -1e-6 {
			t.Fatalf("trial %d: Solve value = %v, brute force = %v (budget %d)", trial, got.Value, want.Value, constraints.BudgetTenths)
		}
	}
}

// TestPositionSuffixDPNeverExceedsFullList is a white-box invariant check
// directly on buildPositionSuffixDP's output — the property the whole fix
// depends on: a suffix table (built over fewer candidates) can never claim
// a higher achievable value than the full-list table (offset 0) at the
// same (k, budget), since dropping candidates from consideration can only
// weakly lower what's achievable. This is what makes the new bound
// provably at least as tight as, and never looser than, the one it
// replaces — checked here directly on the data structure, independent of
// the search that consumes it.
func TestPositionSuffixDPNeverExceedsFullList(t *testing.T) {
	rng := rand.New(rand.NewSource(55))
	candidates := randomCandidates(rng, [5]int{0, 20, 0, 0, 0}, 6) // one position's worth, 20 candidates
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Value > candidates[j].Value })

	const quota = 5
	const maxBudget = 1000
	suffixes := buildPositionSuffixDP(candidates, quota, maxBudget)

	if len(suffixes) != len(candidates)+1 {
		t.Fatalf("got %d suffix tables, want %d (len(candidates)+1)", len(suffixes), len(candidates)+1)
	}

	full := suffixes[0]
	for j := 1; j < len(suffixes); j++ {
		for k := 0; k <= quota; k++ {
			for b := 0; b <= maxBudget; b += 17 { // sample budgets, full sweep is unnecessary for this invariant
				sv := suffixes[j].bestValue(k, b)
				fv := full.bestValue(k, b)
				if sv > fv+1e-9 {
					t.Errorf("offset %d: bestValue(k=%d, b=%d) = %v exceeds full-list bestValue = %v", j, k, b, sv, fv)
				}
			}
		}
	}

	// The empty suffix (offset = len(candidates), nothing left to choose
	// from) must have no way to select anything.
	empty := suffixes[len(candidates)]
	if v := empty.bestValue(0, maxBudget); v != 0 {
		t.Errorf("empty suffix bestValue(0, ...) = %v, want 0", v)
	}
	if v := empty.bestValue(1, maxBudget); v != unreachableValue {
		t.Errorf("empty suffix bestValue(1, ...) = %v, want unreachableValue (nothing available to pick)", v)
	}
}

// realisticFPLCandidates generates a candidate pool at real FPL scale and
// proportions (~180 total, split like the real 2/5/5/3 quota: GKP
// smallest, DEF/MID largest, FWD in between) with price positively
// correlated with value plus noise — mirroring how better real players
// cost more. This also happens to be exactly the pattern that stresses
// budget-driven pruning (the priciest, highest-value candidates get
// excluded first on budget grounds, forcing many decisions deep into a
// block before a feasible combination surfaces), so no separate
// "adversarial" generator is needed.
//
// If clustered, a handful of "big" clubs get a value boost, mimicking
// real FPL's clustering of top performers into a few clubs — the scenario
// randomCandidates' uniform club draw can't exercise, and the one that
// determines whether Phase 2 (a per-club bound, see the plan) is actually
// needed: the current bound structurally ignores the per-club cap, and
// that gap only bites when the best-value candidates cluster by club.
func realisticFPLCandidates(rng *rand.Rand, clustered bool) []Candidate {
	split := [5]int{0, 24, 60, 60, 36}
	const numClubs = 20
	bigClubs := map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true}

	var out []Candidate
	id := 1
	for pos := 1; pos <= 4; pos++ {
		for i := 0; i < split[pos]; i++ {
			club := 1 + rng.Intn(numClubs)
			price := skewedFPLPrice(rng)
			noise := 0.6 + rng.Float64()*0.8
			valueMult := 1.0
			if clustered && bigClubs[club] {
				valueMult = 1.6
			}
			value := float64(price) / 10 * noise * valueMult
			out = append(out, Candidate{ID: id, Position: pos, Club: club, PriceTenths: price, Value: value})
			id++
		}
	}
	return out
}

// skewedFPLPrice draws a price (tenths of a million) shaped like real FPL
// prices: most players are cheap (£4.0-5.5m — squad depth/rotation
// options), a smaller share mid-tier (£5.5-8.0m), and only a few premium
// (£8.0-14.9m). A flat uniform £4-15m draw (what an earlier version of
// this generator used) averages ~£9.45m/player — 15 of them average
// ~£142m, comfortably over any real ~£100m budget, which made "find any
// feasible squad at all" spuriously hard/impossible regardless of how
// good the solver's bounds are. This skew keeps a 15-player squad's
// average cost around £90m — genuinely feasible with real (if modest)
// slack under a £100m budget, matching actual FPL squad-building, where
// the budget constraint bites through a handful of premium picks, not
// through being impossible to meet at all.
func skewedFPLPrice(rng *rand.Rand) int {
	r := rng.Float64()
	switch {
	case r < 0.6:
		return 40 + rng.Intn(16) // £4.0m-5.5m
	case r < 0.9:
		return 55 + rng.Intn(26) // £5.5m-8.0m
	default:
		return 80 + rng.Intn(70) // £8.0m-14.9m
	}
}

// realisticFPLBudgetTenths is a real, generously-slack FPL budget (£100m)
// — deliberately NOT derived from an unconstrained solve's own minimum
// cost the way other tests' budgets are. That distinction matters here:
// a budget pinned to the true minimum feasible cost binds hard and prunes
// aggressively, which masked the real problem during development — an
// unconstrained proof-of-feasibility pass (or any solve with this much
// real-world slack) turned out to still be exponential on clustered data
// even after Phase 1+2 (the suffix-DP and per-club bounds), because
// neither bound alone, nor their min(), is guaranteed tight enough when
// BOTH the budget and the club caps have real room to maneuver at once —
// which is the normal case for an actual FPL squad, not an edge case.
// This is exactly why TimeLimit (see SquadConstraints) exists: below,
// solveAtRealisticScale relies on it rather than asserting a proven
// optimum is always reached quickly.
const realisticFPLBudgetTenths = 1000

// solveAtRealisticScale is shared by the uniform and clustered variants
// below: time a real, slack-budget constrained solve across several seeds
// (branch-and-bound performance is instance-sensitive — a single seed is
// a weak signal), with a TimeLimit as the actual safety net — this is
// anytime search, not exact search, so the assertion is "always returns a
// valid squad within the time budget," not "always proves optimality
// quickly." Optimal is logged, not asserted either way, since which
// seeds finish exactly vs. time out is itself instance-dependent.
func solveAtRealisticScale(t *testing.T, clustered bool) {
	t.Helper()
	quota := [5]int{0, 2, 5, 5, 3}
	seeds := []int64{1, 2, 3, 4, 5}
	const timeLimit = 8 * time.Second
	var worst time.Duration

	for _, seed := range seeds {
		rng := rand.New(rand.NewSource(seed))
		candidates := realisticFPLCandidates(rng, clustered)
		constraints := SquadConstraints{
			BudgetTenths: realisticFPLBudgetTenths, PositionQuota: quota, MaxPerClub: 3, MaxChanges: -1,
			TimeLimit: timeLimit,
		}

		start := time.Now()
		got, nodes, err := solveDebug(candidates, constraints)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("seed %d: Solve: %v", seed, err)
		}
		assertValidSquad(t, got.Squad, constraints)
		t.Logf("seed %d: %d candidates, clustered=%v, %v, %d nodes, optimal=%v", seed, len(candidates), clustered, elapsed, nodes, got.Optimal)
		if elapsed > worst {
			worst = elapsed
		}
	}

	// Generous-but-real ceiling: timeLimit plus headroom for the periodic
	// (not per-node) deadline check's overshoot and general scheduling
	// jitter — a meaningful regression gate against a solve that ignores
	// TimeLimit entirely (a real risk if the deadline-check wiring broke),
	// not a claim about how fast an exact solve is on this data.
	if worst > timeLimit+2*time.Second {
		t.Errorf("worst-case solve took %v across seeds (clustered=%v), want well under TimeLimit(%v)+overshoot", worst, clustered, timeLimit)
	}
}

// TestSolveAtRealisticFPLScale is the real-scale regression test that
// replaces TestSolveTimingAtCurrentScale's old "KNOWN GAP" — quota
// 2/5/5/3, ~180 candidates, uniform random club assignment, a realistic
// slack budget.
func TestSolveAtRealisticFPLScale(t *testing.T) {
	solveAtRealisticScale(t, false)
}

// TestSolveAtRealisticFPLScaleClustered is the scenario that proved Phase
// 1+2's bounds alone aren't sufficient at real FPL scale: the current
// bounds structurally can't account for a slack budget AND club-clustered
// top performers at once, and randomCandidates' uniform club draw can't
// exercise this. TimeLimit (see solveAtRealisticScale) is what actually
// keeps this test — and any real caller hitting the same instance shape —
// bounded.
func TestSolveAtRealisticFPLScaleClustered(t *testing.T) {
	solveAtRealisticScale(t, true)
}

// TestSolveRespectsTimeLimit proves the anytime behavior directly: a
// known-hard instance (clustered, realistic slack budget, seed 2 — the
// one seed of five that still took 5.68s/4.68M nodes to solve exactly
// even after greedyFeasibleSeed made every other real-scale instance
// tested resolve in milliseconds — see solveAtRealisticScale) given a
// short TimeLimit must (a) return quickly, (b) return a fully valid,
// feasible squad regardless, and (c) honestly report Optimal=false —
// confirming the time limit was actually exercised, not that the
// instance happened to be easy.
func TestSolveRespectsTimeLimit(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	candidates := realisticFPLCandidates(rng, true)
	quota := [5]int{0, 2, 5, 5, 3}
	const shortLimit = 200 * time.Millisecond
	constraints := SquadConstraints{
		BudgetTenths: realisticFPLBudgetTenths, PositionQuota: quota, MaxPerClub: 3, MaxChanges: -1,
		TimeLimit: shortLimit,
	}

	start := time.Now()
	got, err := Solve(candidates, constraints)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	assertValidSquad(t, got.Squad, constraints)
	t.Logf("elapsed %v, optimal=%v", elapsed, got.Optimal)

	if elapsed > shortLimit+2*time.Second {
		t.Errorf("Solve took %v, want well under TimeLimit(%v)+overshoot", elapsed, shortLimit)
	}
	if got.Optimal {
		t.Error("got.Optimal = true, want false — this instance is known to run well past shortLimit unbounded, so a true here means the time limit isn't actually being enforced")
	}
}

// TestSolveTimeLimitDoesNotAffectEasyInstances is TestSolveRespectsTimeLimit's
// control: a TimeLimit generous enough to never actually bind must not
// change anything — an easy instance should still finish exactly and
// report Optimal=true, confirming TimeLimit only ever kicks in when it's
// actually needed.
func TestSolveTimeLimitDoesNotAffectEasyInstances(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	candidates := randomCandidates(rng, [5]int{0, 4, 7, 7, 5}, 20)
	quota := [5]int{0, 2, 5, 5, 3}

	unconstrained, err := Solve(candidates, SquadConstraints{BudgetTenths: 1 << 30, PositionQuota: quota, MaxPerClub: 3, MaxChanges: -1})
	if err != nil {
		t.Fatalf("unconstrained solve: %v", err)
	}
	feasibleCost := 0
	for _, c := range unconstrained.Squad {
		feasibleCost += c.PriceTenths
	}
	constraints := SquadConstraints{
		BudgetTenths: feasibleCost, PositionQuota: quota, MaxPerClub: 3, MaxChanges: -1,
		TimeLimit: 30 * time.Second,
	}

	got, err := Solve(candidates, constraints)
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	assertValidSquad(t, got.Squad, constraints)
	if !got.Optimal {
		t.Error("got.Optimal = false on an easy instance with a generous TimeLimit that should never bind")
	}
}

func TestSolveInfeasibleReturnsError(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	candidates := randomCandidates(rng, [5]int{0, 1, 1, 1, 1}, 4) // too few per position
	_, err := Solve(candidates, SquadConstraints{BudgetTenths: 1000, PositionQuota: [5]int{0, 2, 5, 5, 3}, MaxPerClub: 3, MaxChanges: -1})
	if err == nil {
		t.Fatal("want an error when too few candidates exist per position, got nil")
	}
}

// --- test helpers ---

func randomCandidates(rng *rand.Rand, split [5]int, numClubs int) []Candidate {
	var out []Candidate
	id := 1
	for pos := 1; pos <= 4; pos++ {
		for i := 0; i < split[pos]; i++ {
			out = append(out, Candidate{
				ID:          id,
				Position:    pos,
				PriceTenths: 40 + rng.Intn(120),
				Club:        1 + rng.Intn(numClubs),
				Value:       rng.Float64() * 15,
			})
			id++
		}
	}
	return out
}

func assertValidSquad(t *testing.T, squad []Candidate, c SquadConstraints) {
	t.Helper()
	cost := 0
	posCount := map[int]int{}
	clubCount := map[int]int{}
	seen := map[int]bool{}
	for _, cnd := range squad {
		cost += cnd.PriceTenths
		posCount[cnd.Position]++
		clubCount[cnd.Club]++
		if seen[cnd.ID] {
			t.Errorf("duplicate candidate ID %d in squad", cnd.ID)
		}
		seen[cnd.ID] = true
	}
	if cost > c.BudgetTenths {
		t.Errorf("squad cost %d exceeds budget %d", cost, c.BudgetTenths)
	}
	for pos := 1; pos <= numPositions; pos++ {
		if posCount[pos] != c.PositionQuota[pos] {
			t.Errorf("position %d count = %d, want %d", pos, posCount[pos], c.PositionQuota[pos])
		}
	}
	for club, n := range clubCount {
		if n > c.MaxPerClub {
			t.Errorf("club %d has %d players, max %d", club, n, c.MaxPerClub)
		}
	}
}

// bruteForceSolve is a deliberately independent, naive implementation: full
// enumeration of every valid position-quota combination, checked against
// budget and club cap directly, sharing no code with Solve/bnb.go.
func bruteForceSolve(candidates []Candidate, c SquadConstraints) Result {
	byPos := map[int][]Candidate{}
	for _, cnd := range candidates {
		byPos[cnd.Position] = append(byPos[cnd.Position], cnd)
	}

	var combosByPos [][][]Candidate
	for pos := 1; pos <= 4; pos++ {
		combosByPos = append(combosByPos, combinations(byPos[pos], c.PositionQuota[pos]))
	}

	best := Result{Value: math.Inf(-1)}
	var rec func(i int, chosen []Candidate)
	rec = func(i int, chosen []Candidate) {
		if i == len(combosByPos) {
			cost := 0
			club := map[int]int{}
			for _, cnd := range chosen {
				cost += cnd.PriceTenths
				club[cnd.Club]++
			}
			if cost > c.BudgetTenths {
				return
			}
			for _, n := range club {
				if n > c.MaxPerClub {
					return
				}
			}
			val := 0.0
			for _, cnd := range chosen {
				val += cnd.Value
			}
			if val > best.Value {
				best = Result{Squad: append([]Candidate(nil), chosen...), Value: val}
			}
			return
		}
		for _, combo := range combosByPos[i] {
			rec(i+1, append(append([]Candidate(nil), chosen...), combo...))
		}
	}
	rec(0, nil)
	return best
}

// combinations returns every k-element subset of items, order-independent.
func combinations(items []Candidate, k int) [][]Candidate {
	var out [][]Candidate
	n := len(items)
	if k > n || k < 0 {
		return out
	}
	if k == 0 {
		return [][]Candidate{{}}
	}
	idx := make([]int, k)
	for i := range idx {
		idx[i] = i
	}
	for {
		combo := make([]Candidate, k)
		for i, id := range idx {
			combo[i] = items[id]
		}
		out = append(out, combo)

		i := k - 1
		for i >= 0 && idx[i] == i+n-k {
			i--
		}
		if i < 0 {
			break
		}
		idx[i]++
		for j := i + 1; j < k; j++ {
			idx[j] = idx[j-1] + 1
		}
	}
	return out
}
