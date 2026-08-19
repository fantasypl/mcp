package algo

import (
	"fmt"
	"sort"
	"time"
)

// The squad-selection branch-and-bound kernel — deliberately import-free
// from internal/fpl, so it's testable (and brute-forceable) without any
// bootstrap fixture. optimal_squad.go and optimal_transfers.go translate
// fpl.Player into Candidate and call Solve.

// numPositions is how many distinct position slots SquadConstraints.
// PositionQuota covers (index 1..4; index 0 is unused padding, since FPL's
// own element_type values start at 1).
const numPositions = 4

// Candidate is one player as the solver sees it.
type Candidate struct {
	ID          int
	Position    int // 1=GKP 2=DEF 3=MID 4=FWD
	PriceTenths int // price in tenths of a million — integer, not float64, so budget arithmetic is exact
	Club        int
	Value       float64 // the objective: a points-like, additive projection
}

// SquadConstraints bounds one Solve call.
//
// Locked/MaxChanges exist only for optimal_transfers' constrained re-solve:
// a Locked candidate is free to select at zero "change" cost, while every
// other selected candidate counts toward MaxChanges. optimal_squad's
// unconstrained call leaves Locked nil and MaxChanges negative (unlimited).
type SquadConstraints struct {
	BudgetTenths  int
	PositionQuota [5]int
	MaxPerClub    int

	Locked     []int
	MaxChanges int // -1 = unlimited

	// TimeLimit caps how long Solve searches before giving up on proving
	// optimality and returning its best incumbent so far instead — see
	// Result.Optimal. Zero (the default) means no limit: Solve always
	// returns a proven-optimal squad, exactly as it did before this field
	// existed — every caller/test that never sets it is unaffected.
	//
	// This exists because bound() and clubCapBound() (see their doc
	// comments) are each real, tight, provably-safe upper bounds, but on
	// realistic FPL data — a generously-slack budget (most squads don't
	// spend every last penny) combined with club-clustered top performers
	// — neither alone, nor their min(), reliably prunes the search down to
	// a tractable size: measured directly during development, some
	// instances at real FPL scale (2/5/5/3 quota, ~180 candidates) still
	// ran past a minute unbounded. Rather than chase a bound tight enough
	// for every possible input (this file has a history of shipping and
	// reverting bounds that turned out to be merely loose OR outright
	// unsafe — see bound's doc comment), a time limit sidesteps the
	// question entirely: the search already tracks its best feasible
	// incumbent throughout (s.best), so stopping early and returning that
	// is always a valid, fully-constraint-satisfying squad — just not
	// guaranteed to be the best possible one.
	TimeLimit time.Duration
}

// Result is one feasible squad and its total value.
type Result struct {
	Squad []Candidate
	Value float64
	// Optimal is false only when TimeLimit was set and reached before the
	// search could prove optimality — Squad is still fully valid and
	// feasible (the best one found before time ran out), just not
	// guaranteed to be the best possible one. Always true when TimeLimit
	// is zero (unlimited).
	Optimal bool
}

// Solve returns the value-maximizing squad satisfying c's budget,
// per-position quota, per-club cap, and (if set) locked/change-count
// constraints. Returns an error if no feasible squad exists — too few
// candidates in some position, or the budget can't stretch to fill every
// quota even with the cheapest available candidates.
func Solve(candidates []Candidate, c SquadConstraints) (Result, error) {
	s, err := solve(candidates, c)
	if err != nil {
		return Result{}, err
	}
	return s.best, nil
}

// solveDebug behaves exactly like Solve but also returns the number of
// recurse() calls made — a deterministic, hardware-independent proxy for
// search size, used by tests that need to regression-test pruning
// effectiveness (wall-clock varies too much run to run to trust for that).
func solveDebug(candidates []Candidate, c SquadConstraints) (Result, int, error) {
	s, err := solve(candidates, c)
	if err != nil {
		return Result{}, 0, err
	}
	return s.best, s.nodeCount, nil
}

// solve is Solve's shared core, returning the solver itself (best result
// plus internal search stats) rather than just the caller-facing Result —
// Solve and solveDebug are thin wrappers over this.
func solve(candidates []Candidate, c SquadConstraints) (*solver, error) {
	if err := validateConstraints(c); err != nil {
		return nil, err
	}
	locked := toSet(c.Locked)

	var byPosition [5][]Candidate
	for _, cnd := range candidates {
		if cnd.Position < 1 || cnd.Position > numPositions {
			continue
		}
		byPosition[cnd.Position] = append(byPosition[cnd.Position], cnd)
	}
	pruned := pruneDominated(byPosition, locked, c.PositionQuota)

	for pos := 1; pos <= numPositions; pos++ {
		if len(pruned[pos]) < c.PositionQuota[pos] {
			return nil, fmt.Errorf("position %d: only %d candidates available, need %d", pos, len(pruned[pos]), c.PositionQuota[pos])
		}
	}

	// Value-descending per position — both the branch order (below) and
	// bound's per-position relaxation (see bound's doc comment) rely on
	// this ordering.
	var byPositionSorted [5][]Candidate
	for pos := 1; pos <= numPositions; pos++ {
		group := append([]Candidate(nil), pruned[pos]...)
		sort.Slice(group, func(i, j int) bool { return group[i].Value > group[j].Value })
		byPositionSorted[pos] = group
	}

	// Branch order: GKP, FWD, MID, DEF — smallest/cheapest quota first, so
	// the search finds a strong incumbent (and starts pruning against it)
	// as early as possible. Within each position, value-descending, so the
	// first combinations tried are near-optimal.
	pool := make([]Candidate, 0, len(candidates))
	var blockStart [5]int // pool index where each position's block begins — maps a node's idx to an offset within its position's own sorted list
	for _, pos := range [...]int{1, 4, 3, 2} {
		blockStart[pos] = len(pool)
		pool = append(pool, byPositionSorted[pos]...)
	}

	// A suffix family of knapsack-exact-count DP tables per position, built
	// once here and queried (O(1) per lookup) by bound() at every node —
	// see buildPositionSuffixDP's doc comment for what it computes and why
	// a family (not one global table) is needed.
	//
	// Each table is sized to min(c.BudgetTenths, that position's own
	// maximum possible cost) rather than c.BudgetTenths directly — table
	// size is O(budget), and c.BudgetTenths can be enormous (callers using
	// it as a practically-unlimited sentinel, e.g. to find an unconstrained
	// optimum). No valid quota[pos]-sized selection from a position can
	// ever cost more than its quota[pos] most expensive candidates summed,
	// so capping there loses no correctness while keeping table size tied
	// to real candidate prices instead of an arbitrary caller-supplied
	// budget figure.
	var dpSuffix [5][]positionDP
	totalMaxCost := 0
	for pos := 1; pos <= numPositions; pos++ {
		posMax := positionMaxCost(byPositionSorted[pos], c.PositionQuota[pos])
		totalMaxCost += posMax
		tableBudget := c.BudgetTenths
		if posMax < tableBudget {
			tableBudget = posMax
		}
		dpSuffix[pos] = buildPositionSuffixDP(byPositionSorted[pos], c.PositionQuota[pos], tableBudget)
	}

	// bound's budget-sharing merge (see its doc comment) needs a single
	// common budget range to combine positions' DP rows over — same
	// reasoning as each table's own cap above: use the real cost ceiling,
	// not a possibly-enormous caller-supplied budget.
	mergeCap := c.BudgetTenths
	if totalMaxCost < mergeCap {
		mergeCap = totalMaxCost
	}
	blockOrder := [numPositions]int{1, 4, 3, 2}
	var blockIndexOf [5]int
	for i, pos := range blockOrder {
		blockIndexOf[pos] = i
	}
	var fullRow [5][]float64
	for pos := 1; pos <= numPositions; pos++ {
		row := make([]float64, mergeCap+1)
		for b := 0; b <= mergeCap; b++ {
			// dpSuffix[pos][0] covers pos's entire candidate list (offset 0
			// = nothing excluded yet) — the same table the old single
			// global build produced.
			row[b] = dpSuffix[pos][0].bestValue(c.PositionQuota[pos], b)
		}
		fullRow[pos] = row
	}
	// suffixAfter[i] = the best combined value from every position AFTER
	// blockOrder[i] in branch order, each at its full original quota,
	// sharing a budget — computed back-to-front so each merge only ever
	// combines two already-computed pieces.
	var suffixAfter [numPositions][]float64
	suffixAfter[numPositions-1] = make([]float64, mergeCap+1) // nothing after the last block
	for i := numPositions - 2; i >= 0; i-- {
		suffixAfter[i] = mergeValueArrays(fullRow[blockOrder[i+1]], suffixAfter[i+1], mergeCap)
	}

	// Phase 2: an independent, club-cap-only bound, precomputed the same
	// node-aware way as dpSuffix above — clubTopSuffix[pos][j] mirrors
	// dpSuffix[pos][j] exactly, just tracking each club's top MaxPerClub
	// values instead of a value/budget DP (see buildClubTopSuffix).
	// clubTopAfter[bi] mirrors suffixAfter — the merged top values from
	// every position strictly after blockOrder[bi], full lists, since
	// those are always untouched at any node (same invariant bound's own
	// doc comment relies on). clubTopByIdx[idx] is the final per-node
	// combination, precomputed once per pool index (not per DFS node —
	// see clubCapBound's doc comment for why that's sound and sufficient).
	var clubTopSuffix [5][]map[int][]float64
	for pos := 1; pos <= numPositions; pos++ {
		clubTopSuffix[pos] = buildClubTopSuffix(byPositionSorted[pos], c.MaxPerClub)
	}
	var clubTopAfter [numPositions]map[int][]float64
	clubTopAfter[numPositions-1] = map[int][]float64{}
	for i := numPositions - 2; i >= 0; i-- {
		afterPos := blockOrder[i+1]
		clubTopAfter[i] = mergeClubTops(clubTopSuffix[afterPos][0], clubTopAfter[i+1], c.MaxPerClub)
	}
	clubTopByIdx := make([]map[int][]float64, len(pool))
	for idx, cnd := range pool {
		bi := blockIndexOf[cnd.Position]
		j := idx - blockStart[cnd.Position]
		clubTopByIdx[idx] = mergeClubTops(clubTopSuffix[cnd.Position][j], clubTopAfter[bi], c.MaxPerClub)
	}

	s := &solver{
		pool: pool, dpSuffix: dpSuffix, blockStart: blockStart, mergeCap: mergeCap,
		suffixAfter: suffixAfter, blockIndexOf: blockIndexOf, clubTopByIdx: clubTopByIdx,
		locked: locked, c: c, best: Result{Value: negInf},
	}
	// Seed s.best with a cheap, ratio-greedy constructive squad before the
	// exhaustive search starts — see greedyFeasibleSeed's doc comment for
	// why this is safe (unlike a greedy BOUND, a greedy CONSTRUCTION only
	// needs to be feasible, never provably tight) and why it's needed
	// (value correlates with price in real FPL data — better players cost
	// more — so the search's own value-descending branch order tends to
	// try the priciest combinations first and can burn enormous time
	// backtracking to ANY affordable complete squad, even before TimeLimit
	// has a real incumbent to fall back on).
	if seed, ok := greedyFeasibleSeed(byPositionSorted, c); ok {
		s.best = seed
	}
	if c.TimeLimit > 0 {
		s.deadline = time.Now().Add(c.TimeLimit)
	}
	s.recurse(0, nil, 0, c.PositionQuota, map[int]int{}, 0)

	if s.best.Value == negInf {
		if s.timedOut {
			return nil, fmt.Errorf("time limit (%v) reached before finding any feasible squad", c.TimeLimit)
		}
		return nil, fmt.Errorf("no feasible squad found under the given budget and constraints")
	}
	// Set once, here, rather than per-incumbent-update inside recurse:
	// whatever s.best held when the search stopped is exactly what's
	// returned, so "did the search finish" is all Optimal needs to
	// capture — no per-update bookkeeping required.
	s.best.Optimal = !s.timedOut

	// Deterministic output ordering — the search itself doesn't guarantee
	// one, and callers (golden fixtures in particular) need stability.
	sort.Slice(s.best.Squad, func(i, j int) bool {
		if s.best.Squad[i].Position != s.best.Squad[j].Position {
			return s.best.Squad[i].Position < s.best.Squad[j].Position
		}
		return s.best.Squad[i].ID < s.best.Squad[j].ID
	})
	return s, nil
}

// greedyFeasibleSeed constructs a simple feasible squad, filling each
// position's quota by value/price ratio (not raw value) — deliberately
// budget-aware, unlike the search's own branch order (value-descending,
// see Solve's doc comment on branch order), which on real FPL data tends
// to try the priciest combinations first (value correlates with price:
// better players cost more) and can spend enormous effort backtracking
// before completing ANY affordable full squad. This exists purely to
// seed s.best with a starting incumbent before the exhaustive search
// begins, so a TimeLimit search always has a valid answer to fall back on
// even if it times out before independently completing a path — measured
// directly during development: without this, a real-scale (2/5/5/3
// quota, ~180 candidates), realistically-slack-budget instance visited
// 6.48M nodes in 3 seconds without completing a single leaf.
//
// This is a CONSTRUCTIVE heuristic, not a bound — it never participates
// in pruning (bound() and clubCapBound() remain the only things recurse
// prunes against, and both stay exact, safe upper bounds regardless of
// what seeds s.best), so none of bound's "greedy is unsafe" cautionary
// history applies: a greedy construction's only possible failure mode is
// being suboptimal, never unsound, since ok=true is only ever returned
// once every quota, budget, and club-cap check below has already passed.
// Seeding a better initial incumbent only makes recurse's own pruning
// kick in sooner — it cannot change what the search is capable of
// eventually proving, only how quickly it gets there.
//
// Returns ok=false if this simple construction can't complete a feasible
// squad (e.g. a position's cheapest-by-ratio candidates still don't fit
// remaining budget) — harmless either way, since recurse's own exhaustive
// search remains the source of truth regardless of whether a seed exists.
func greedyFeasibleSeed(byPositionSorted [5][]Candidate, c SquadConstraints) (Result, bool) {
	locked := toSet(c.Locked)
	spent := 0
	changesUsed := 0
	clubCount := map[int]int{}
	var squad []Candidate

	for pos := 1; pos <= numPositions; pos++ {
		group := append([]Candidate(nil), byPositionSorted[pos]...)
		sort.Slice(group, func(i, j int) bool {
			// Locked candidates first — free of MaxChanges cost, so
			// there's never a reason to prefer a non-locked alternative
			// over one already secured. Otherwise value/price descending.
			li, lj := locked[group[i].ID], locked[group[j].ID]
			if li != lj {
				return li
			}
			return group[i].Value/float64(group[i].PriceTenths) > group[j].Value/float64(group[j].PriceTenths)
		})

		picked := 0
		for _, cnd := range group {
			if picked >= c.PositionQuota[pos] {
				break
			}
			isLocked := locked[cnd.ID]
			newChanges := changesUsed
			if !isLocked {
				newChanges++
			}
			if !isLocked && c.MaxChanges >= 0 && newChanges > c.MaxChanges {
				continue
			}
			if spent+cnd.PriceTenths > c.BudgetTenths {
				continue
			}
			if clubCount[cnd.Club]+1 > c.MaxPerClub {
				continue
			}
			squad = append(squad, cnd)
			spent += cnd.PriceTenths
			clubCount[cnd.Club]++
			changesUsed = newChanges
			picked++
		}
		if picked < c.PositionQuota[pos] {
			return Result{}, false
		}
	}
	return Result{Squad: squad, Value: sumValue(squad)}, true
}

const negInf = -(1 << 62) // a value no real squad total can reach, used as "no result yet"

type solver struct {
	pool         []Candidate             // branch order: position blocks, value-descending within each
	dpSuffix     [5][]positionDP         // dpSuffix[pos][j]: exact-count DP over pos's own sorted list from offset j onward — see buildPositionSuffixDP
	blockStart   [5]int                  // pool index where each position's block begins — maps a node's idx to its offset j within that block
	mergeCap     int                     // common budget range suffixAfter's rows are sized to
	suffixAfter  [numPositions][]float64 // suffixAfter[i]: best combined value of every position after blockOrder[i], full quota, sharing budget
	blockIndexOf [5]int                  // position -> its index in the fixed branch order
	clubTopByIdx []map[int][]float64     // clubTopByIdx[idx]: club -> top MaxPerClub still-available values at that node — see clubCapBound
	locked       map[int]bool
	c            SquadConstraints
	best         Result
	nodeCount    int       // recurse() call count — see solveDebug
	deadline     time.Time // zero = no limit; see SquadConstraints.TimeLimit
	timedOut     bool      // set once the deadline is observed — see recurse's timeCheckInterval check
}

// timeCheckInterval is how many recurse() calls pass between deadline
// checks, when a deadline is set — time.Now() isn't free, and a
// once-per-node check would add real overhead across the millions of
// nodes a hard instance can visit. This bounds how far the search can
// overshoot its deadline (worst case: one interval's worth of work,
// microseconds even at pathological scale) in exchange for negligible
// per-node cost the rest of the time.
const timeCheckInterval = 4096

// recurse makes one include/exclude decision on pool[idx] per call — a
// standard depth-first branch-and-bound over a fixed item order. chosen,
// spent, posLeft, clubCount, and changesUsed together describe the partial
// solution up to (not including) idx.
func (s *solver) recurse(idx int, chosen []Candidate, spent int, posLeft [5]int, clubCount map[int]int, changesUsed int) {
	if s.timedOut {
		return // already given up — cascade back up the call stack cheaply, no further work per frame
	}
	s.nodeCount++
	if !s.deadline.IsZero() && s.nodeCount%timeCheckInterval == 0 && time.Now().After(s.deadline) {
		s.timedOut = true
		return
	}
	needed := 0
	for _, n := range posLeft {
		needed += n
	}
	currentValue := sumValue(chosen)
	if needed == 0 {
		if currentValue > s.best.Value {
			s.best = Result{Squad: append([]Candidate(nil), chosen...), Value: currentValue}
		}
		return
	}
	if idx >= len(s.pool) {
		return // ran out of candidates with quota still unfilled — infeasible branch
	}
	cnd := s.pool[idx]
	// j: cnd's offset within its own position's sorted list — every earlier
	// candidate in this block (offsets < j) has already been decided
	// (included, reflected in chosen/spent/posLeft, or excluded, gone for
	// good) by the DFS reaching this node, so dpSuffix[cnd.Position][j] is
	// exactly the right "still available" table to bound against.
	j := idx - s.blockStart[cnd.Position]
	currentDP := s.dpSuffix[cnd.Position][j]

	// Two independent, differently-relaxed upper bounds on the same true
	// value: posBound is exact on price/budget/position-quota, ignoring
	// only the club cap; clBound is exact on the club cap alone, ignoring
	// price/budget/position-quota entirely. Their min is still a valid
	// upper bound (the real, fully-constrained optimum is <= both), and
	// combining them this way needs no reasoning about their interaction —
	// see clubCapBound's doc comment for why that matters here.
	posBound := bound(currentDP, s.suffixAfter, s.blockIndexOf, s.mergeCap, cnd.Position, posLeft, s.c.BudgetTenths-spent, currentValue)
	clBound := clubCapBound(s.clubTopByIdx[idx], clubCount, s.c.MaxPerClub, currentValue)
	nodeBound := posBound
	if clBound < nodeBound {
		nodeBound = clBound
	}
	if nodeBound <= s.best.Value {
		return // even the best possible completion can't beat the incumbent
	}

	// Option 1: include cnd.
	if posLeft[cnd.Position] > 0 && spent+cnd.PriceTenths <= s.c.BudgetTenths {
		newChanges := changesUsed
		if !s.locked[cnd.ID] {
			newChanges++
		}
		if s.c.MaxChanges < 0 || newChanges <= s.c.MaxChanges {
			if clubCount[cnd.Club]+1 <= s.c.MaxPerClub {
				newClub := cloneClubCount(clubCount)
				newClub[cnd.Club]++
				newPosLeft := posLeft
				newPosLeft[cnd.Position]--
				s.recurse(idx+1, append(chosen, cnd), spent+cnd.PriceTenths, newPosLeft, newClub, newChanges)
			}
		}
	}

	// Option 2: exclude cnd — chosen/spent/posLeft/clubCount/changesUsed
	// are unchanged, since append() in option 1 never mutates chosen's own
	// length as seen from this call frame.
	s.recurse(idx+1, chosen, spent, posLeft, clubCount, changesUsed)
}

// bound computes an optimistic (never-underestimating) upper bound on the
// best total value reachable from a partial solution: currentValue already
// secured, posLeft more candidates needed per position, budgetLeft
// remaining, currently deciding a candidate at position currentPos.
//
// Because Solve processes positions in a FIXED sequential block order
// (blockIndexOf's order — GKP, FWD, MID, DEF), at any node at most one
// position (currentPos) is partially resolved; every position later in
// that order is still completely untouched (full original quota), and
// every position earlier in it has already been fully decided one way or
// the other. That structure is what makes a tight, budget-shared bound
// affordable to compute per node:
//
//   - Any earlier position still needing items can never be filled (all
//     its candidates were already decided) — the branch is dead; return
//     unreachableValue immediately.
//   - currentPos (posLeft[currentPos] more needed) and everything later
//     (each at full quota, precomputed once as suffixAfter — see Solve)
//     are combined by trying every way to split budgetLeft between "spend
//     b1 on currentPos" and "spend the rest on everything after",
//     keeping the best split. This is exact for that two-way split (both
//     sides are themselves exact, club-cap-ignoring optima), so the
//     result is the true best value achievable ignoring only club caps —
//     tighter than treating every position as if it had independent
//     access to the full remaining budget, which is what an earlier
//     version of this bound did.
//
// Two even earlier, less careful attempts at a budget-aware bound were
// UNSAFE (not merely loose), worth naming so the mistake doesn't get
// remade: assigning candidates by global value-per-price and stopping each
// position once its own quota filled never reconsiders a pricier,
// higher-value candidate for that position later in ratio order, even
// when the true optimum affords it by spending less elsewhere — true
// whether the per-position cap is exact or relaxed to a single combined
// count. Solving each position's own exact-count/exact-budget problem via
// DP (not greedy-by-ratio) has no such interaction to get wrong.
//
// currentPosDP must be the caller's dpSuffix[currentPos][j] for cnd's own
// offset j within currentPos's block (see recurse) — NOT a table built
// over currentPos's full candidate list. Using the full-list table here
// would let already-excluded-earlier-in-this-branch candidates count as
// available, loosening the bound; the suffix-scoped table restricts
// currentPosDP.bestValue to genuinely still-available candidates only,
// so this stays a valid upper bound while being at least as tight as (and,
// deep inside a large block, materially tighter than) a full-list table
// would give.
func bound(currentPosDP positionDP, suffixAfter [numPositions][]float64, blockIndexOf [5]int, mergeCap int, currentPos int, posLeft [5]int, budgetLeft int, currentValue float64) float64 {
	bi := blockIndexOf[currentPos]
	for pos := 1; pos <= numPositions; pos++ {
		if pos != currentPos && blockIndexOf[pos] < bi && posLeft[pos] > 0 {
			return unreachableValue // an earlier block still needs items — permanently infeasible
		}
	}
	if budgetLeft < 0 {
		return unreachableValue
	}

	suffix := suffixAfter[bi]
	maxB2 := budgetLeft
	if maxB2 > mergeCap {
		maxB2 = mergeCap
	}
	best := unreachableValue
	for b2 := 0; b2 <= maxB2; b2++ {
		sv := suffix[b2]
		if sv == unreachableValue {
			continue
		}
		cv := currentPosDP.bestValue(posLeft[currentPos], budgetLeft-b2)
		if cv == unreachableValue {
			continue
		}
		if cand := sv + cv; cand > best {
			best = cand
		}
	}
	if best == unreachableValue {
		return unreachableValue
	}
	return currentValue + best
}

// mergeValueArrays combines two "best value for total cost <= b" arrays
// (same semantics as positionDP.bestValue's output, monotonic non-
// decreasing in b) into one representing their combination sharing a
// single budget: result[b] = max over b2 in [0,b] of a[b-b2] + bb[b2].
// Both inputs must already use "at most" (not "exactly") semantics — the
// output does too, so repeated merges compose correctly.
func mergeValueArrays(a, bb []float64, cap int) []float64 {
	out := make([]float64, cap+1)
	for budget := 0; budget <= cap; budget++ {
		best := unreachableValue
		for b2 := 0; b2 <= budget; b2++ {
			bv := bb[b2]
			if bv == unreachableValue {
				continue
			}
			av := a[budget-b2]
			if av == unreachableValue {
				continue
			}
			if cand := av + bv; cand > best {
				best = cand
			}
		}
		out[budget] = best
	}
	return out
}

// positionDP answers, for one candidate list: what's the best total value
// from choosing exactly k of them with total cost ≤ b? Queried in O(1)
// once built.
type positionDP struct {
	maxBudget int
	// table[k][b] = best value for exactly k candidates costing <= b.
	// table[0][*] = 0. An entry that's still unreachableValue means no
	// combination of k candidates fits within b.
	table [][]float64
}

const unreachableValue = -1e18

// buildPositionSuffixDP builds one positionDP per suffix of candidates
// (already this position's pruned, value-descending list): result[j]
// answers "best value from exactly k of candidates[j:], costing <= b".
// result[0] covers the whole list (what a single global table would have
// been); result[len(candidates)] is the empty-selection table (k=0 only).
//
// This family — not one global table — exists so bound() can query, at
// any branch-and-bound node, a table restricted to candidates NOT YET
// decided in that branch. Candidates within one position's pool block are
// visited value-descending in a fixed order, so "not yet decided" is
// always a known suffix (see recurse's j = idx - blockStart[pos]). A
// single global table let already-excluded-earlier-in-branch candidates
// still count as available, loosening the bound exactly where large-quota
// positions (MID, DEF) have the most in-block decision points to lose
// precision over — see bound's doc comment for the pruning consequence.
// Any suffix's table can never exceed the full-list table at the same
// (k, budget): dropping candidates from consideration can only weakly
// lower the best achievable value, never raise it — so every entry in
// this family is provably a valid upper bound, and at least as tight as
// (materially tighter than, deep in a block) the single table it replaces.
//
// Built backward, one candidate at a time: fold candidates[j] into a
// single running "cost exactly b" table (same 0/1 knapsack update
// buildPositionSuffixDP's single-table predecessor used), then snapshot a
// non-destructive, "cost at most b"-converted copy as result[j]. Kept in
// "exact" form between insertions (never prefix-maxed in place) so later
// insertions stay correct — only each snapshot copy is converted. Total
// cost is O(len(candidates) × quota × budget) — the same order building
// ONE global table would have cost, not len(candidates) times worse,
// since each insertion extends the previous step's table rather than
// rebuilding from scratch.
// clubCapBound computes an optimistic upper bound on additional value
// achievable from the still-available candidates in clubTop (precomputed
// per node — see buildClubTopSuffix and mergeClubTops), enforcing ONLY
// the per-club cap (maxPerClub) and ignoring price/budget and
// position-quota constraints entirely: for each club, at most
// maxPerClub-clubCount[club] more of its candidates could ever be
// selected, so summing each club's top that-many still-available values
// is the true optimum of exactly that relaxed problem — no candidates
// need considering beyond a club's own top maxPerClub (a club can never
// contribute more than maxPerClub picks in any real feasible squad,
// regardless of price or position), which is exactly why clubTop only
// ever needs to track each club's top maxPerClub values, not its full
// list.
//
// This is deliberately a DIFFERENT, independent relaxation from bound()'s
// (which does the reverse: exact on price/budget/position-quota, ignoring
// the club cap). min() of two independently-sound relaxations of the same
// true value is itself sound — the real, fully-constrained optimum is <=
// both, hence <= their min — with no new reasoning needed about how the
// two relaxations interact.
//
// Safe where two much earlier value/price-ratio-greedy bound attempts
// were proven UNSAFE (see bound's doc comment): those failed because 0/1
// knapsack (a budget/price constraint) is NOT a matroid, so greedy isn't
// provably optimal for it. This bound has no budget/price dimension at
// all — "pick the highest-value items subject to a per-club cap" is a
// partition matroid, and greedy-by-value IS provably optimal on a
// matroid. It isn't approximating a hard problem; it's exactly solving an
// easy one, which is why no interaction-with-price bug is possible here.
func clubCapBound(clubTop map[int][]float64, clubCount map[int]int, maxPerClub int, currentValue float64) float64 {
	total := currentValue
	for club, vals := range clubTop {
		remaining := maxPerClub - clubCount[club]
		if remaining <= 0 {
			continue
		}
		n := remaining
		if n > len(vals) {
			n = len(vals)
		}
		for i := 0; i < n; i++ {
			total += vals[i]
		}
	}
	return total
}

// buildClubTopSuffix mirrors buildPositionSuffixDP's backward-incremental
// approach, but for the much smaller structure clubCapBound needs:
// result[j] = each club's top maxPerClub values among candidates[j:] —
// the same "node-aware, not yet decided only" semantics dpSuffix
// provides, since a club can never usefully keep more than maxPerClub
// candidates in view regardless of how many it actually has available.
// Built with the same immutable-snapshot discipline as
// buildPositionSuffixDP for the same reason: each result[j] must stay
// valid independent of later (smaller-j) insertions.
func buildClubTopSuffix(candidates []Candidate, maxPerClub int) []map[int][]float64 {
	m := len(candidates)
	running := map[int][]float64{}
	result := make([]map[int][]float64, m+1)
	result[m] = map[int][]float64{} // nothing available
	for j := m - 1; j >= 0; j-- {
		cnd := candidates[j]
		vals := append(append([]float64(nil), running[cnd.Club]...), cnd.Value)
		sort.Sort(sort.Reverse(sort.Float64Slice(vals)))
		if len(vals) > maxPerClub {
			vals = vals[:maxPerClub]
		}
		snap := make(map[int][]float64, len(running)+1)
		for c, v := range running {
			snap[c] = v
		}
		snap[cnd.Club] = vals
		running = snap
		result[j] = snap
	}
	return result
}

// mergeClubTops combines two "top maxPerClub values per club" maps into
// one covering their union — per club, concatenating both lists then
// keeping only the top maxPerClub. Used both to fold a position's own
// full-list top-values into a broader "after" accumulator (mirroring
// suffixAfter) and to combine a node's own position-suffix top-values
// with that accumulator into the final per-node clubTop.
func mergeClubTops(a, b map[int][]float64, maxPerClub int) map[int][]float64 {
	out := make(map[int][]float64, len(a)+len(b))
	seen := make(map[int]bool, len(a)+len(b))
	for club := range a {
		seen[club] = true
	}
	for club := range b {
		seen[club] = true
	}
	for club := range seen {
		merged := append(append([]float64(nil), a[club]...), b[club]...)
		sort.Sort(sort.Reverse(sort.Float64Slice(merged)))
		if len(merged) > maxPerClub {
			merged = merged[:maxPerClub]
		}
		out[club] = merged
	}
	return out
}

func buildPositionSuffixDP(candidates []Candidate, quota int, maxBudget int) []positionDP {
	if maxBudget < 0 {
		maxBudget = 0
	}
	m := len(candidates)
	raw := make([][]float64, quota+1)
	for k := range raw {
		raw[k] = make([]float64, maxBudget+1)
		if k > 0 {
			for b := range raw[k] {
				raw[k][b] = unreachableValue
			}
		}
	}

	suffixes := make([]positionDP, m+1)
	suffixes[m] = snapshotAtMost(raw, maxBudget)
	for j := m - 1; j >= 0; j-- {
		insertIntoExactTable(raw, candidates[j], quota, maxBudget)
		suffixes[j] = snapshotAtMost(raw, maxBudget)
	}
	return suffixes
}

// insertIntoExactTable folds one more candidate into raw's "cost exactly
// b" knapsack table, mutating it in place — the standard backward k/b
// 0/1-knapsack update. raw is kept in this exact (never prefix-maxed)
// form throughout buildPositionSuffixDP's insertion loop so later
// insertions stay correct; only a snapshot copy (see snapshotAtMost) is
// ever converted to "at most" semantics.
func insertIntoExactTable(raw [][]float64, cnd Candidate, quota, maxBudget int) {
	price := cnd.PriceTenths
	if price > maxBudget {
		return // can never be afforded regardless of what else is picked
	}
	for k := quota; k >= 1; k-- {
		for b := maxBudget; b >= price; b-- {
			prev := raw[k-1][b-price]
			if prev == unreachableValue {
				continue
			}
			if v := prev + cnd.Value; v > raw[k][b] {
				raw[k][b] = v
			}
		}
	}
}

// snapshotAtMost copies raw (still in "cost exactly b" form) and converts
// the copy to "cost at most b" via a per-row prefix max, so bestValue can
// look up any budget directly — non-destructively, so raw itself is left
// untouched for buildPositionSuffixDP's next insertion to build on.
func snapshotAtMost(raw [][]float64, maxBudget int) positionDP {
	table := make([][]float64, len(raw))
	for k := range raw {
		row := make([]float64, len(raw[k]))
		copy(row, raw[k])
		for b := 1; b <= maxBudget; b++ {
			if row[b-1] > row[b] {
				row[b] = row[b-1]
			}
		}
		table[k] = row
	}
	return positionDP{maxBudget: maxBudget, table: table}
}

// bestValue returns the best value for exactly k candidates costing at
// most budget — unreachableValue (very negative, never a valid squad
// total) if no such combination exists, which is intentional: summed into
// bound(), it drives that branch's overall bound low enough to be pruned,
// correctly reflecting that the branch is infeasible.
func (dp positionDP) bestValue(k, budget int) float64 {
	if k <= 0 {
		return 0
	}
	if k >= len(dp.table) {
		k = len(dp.table) - 1
	}
	if budget > dp.maxBudget {
		budget = dp.maxBudget
	}
	if budget < 0 {
		return unreachableValue
	}
	return dp.table[k][budget]
}

// pruneDominated discards a candidate only when it can never be needed to
// fill its position's exact quota — not the same test as ordinary
// knapsack dominance, and not safe to do across clubs, either. Two
// separate reasons a naive "dominated, so discard" rule fails here:
//
//  1. This problem must fill an EXACT count k per position, not "as many
//     as you like." A cheaper-and-better candidate A "beats" B for a
//     single free slot, but if B is position P's 3rd-best affordable
//     option and P needs exactly 3, discarding B because A beats it in
//     isolation would make the position unfillable even though B was the
//     correct 3rd pick. Fix: B is only safe to discard if at least k
//     OTHER candidates each individually dominate it (cheaper-or-equal
//     price, higher-or-equal value, at least one strict) — with k
//     dominators, any valid k-sized selection using B can be rewritten to
//     swap B for a dominator not already selected (there's always at
//     least one, by pigeonhole: k dominators can't all fit in the k-1
//     other slots).
//  2. That swap argument silently assumed the dominator is always usable
//     as a substitute — but the per-club cap is a constraint that spans
//     *across* positions, and a dominator sharing B's price/value
//     advantage can still belong to a club that's already at its cap
//     from players chosen in OTHER positions, making the swap infeasible
//     in that specific squad even though it looks strictly better in
//     isolation. (Caught by a failing brute-force comparison during
//     development: a winning squad already had 3 players from one club
//     via other positions, and the "dominator" being pruned in its place
//     belonged to that same club — an actually-infeasible substitute.)
//     Fix: a dominator only counts if it shares B's OWN club. Swapping
//     within one club can never change that club's total count, so it
//     can never introduce a club violation regardless of what's chosen
//     elsewhere — the one interaction that broke the general case is
//     structurally impossible here.
//
// With k=1 and same-club-only dominators, this reduces to a narrow
// same-club Pareto frontier — a safe but much more conservative prune than
// the plan's original ~700-to-~150 estimate assumed (that estimate implied
// unconstrained dominance, which turned out to be unsound once a per-club
// cap is in the constraint set). Tightening this further — a genuinely
// safe cross-club rule — is a targeted follow-up if profiling ever shows
// it's needed; getting a provably-safe prune was the priority here.
//
// Locked candidates are always kept regardless of dominance count: they
// have a different effective cost (selecting them doesn't count against
// MaxChanges) that price/value dominance doesn't account for, so discarding
// one could make an otherwise-reachable, hit-free squad invisible to the
// search.
func pruneDominated(byPosition [5][]Candidate, locked map[int]bool, quota [5]int) [5][]Candidate {
	var out [5][]Candidate
	for pos := 1; pos <= numPositions; pos++ {
		group := byPosition[pos]
		k := quota[pos]
		var frontier []Candidate
		for _, b := range group {
			if locked[b.ID] {
				frontier = append(frontier, b)
				continue
			}
			dominators := 0
			for _, a := range group {
				if a.ID == b.ID || a.Club != b.Club {
					continue
				}
				if dominatesCandidate(a, b) {
					dominators++
					if dominators >= k {
						break
					}
				}
			}
			if dominators < k {
				frontier = append(frontier, b)
			}
		}
		out[pos] = frontier
	}
	return out
}

// dominatesCandidate reports whether a is at least as good as b on both
// price and value, with at least one strict — a is cheaper-or-equal AND
// higher-or-equal value, genuinely better on at least one axis.
func dominatesCandidate(a, b Candidate) bool {
	return a.PriceTenths <= b.PriceTenths && a.Value >= b.Value &&
		(a.PriceTenths < b.PriceTenths || a.Value > b.Value)
}

func validateConstraints(c SquadConstraints) error {
	if c.BudgetTenths <= 0 {
		return fmt.Errorf("budget must be positive, got %d", c.BudgetTenths)
	}
	total := 0
	for pos := 1; pos <= numPositions; pos++ {
		if c.PositionQuota[pos] < 0 {
			return fmt.Errorf("position %d quota must be non-negative, got %d", pos, c.PositionQuota[pos])
		}
		total += c.PositionQuota[pos]
	}
	if total == 0 {
		return fmt.Errorf("position quotas sum to zero")
	}
	if c.MaxPerClub <= 0 {
		return fmt.Errorf("max per club must be positive, got %d", c.MaxPerClub)
	}
	return nil
}

// positionMaxCost returns the sum of the k most expensive candidates'
// prices — the highest total any exactly-k selection from this list could
// possibly cost, used to cap a positionDP table's size safely regardless
// of how large the caller's actual budget figure is.
func positionMaxCost(candidates []Candidate, k int) int {
	if k <= 0 {
		return 0
	}
	prices := make([]int, len(candidates))
	for i, c := range candidates {
		prices[i] = c.PriceTenths
	}
	sort.Sort(sort.Reverse(sort.IntSlice(prices)))
	if k > len(prices) {
		k = len(prices)
	}
	sum := 0
	for i := 0; i < k; i++ {
		sum += prices[i]
	}
	return sum
}

func sumValue(candidates []Candidate) float64 {
	total := 0.0
	for _, cnd := range candidates {
		total += cnd.Value
	}
	return total
}

func toSet(ids []int) map[int]bool {
	out := make(map[int]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func cloneClubCount(m map[int]int) map[int]int {
	out := make(map[int]int, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}
