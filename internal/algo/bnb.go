package algo

import (
	"fmt"
	"sort"
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
}

// Result is one feasible squad and its total value.
type Result struct {
	Squad []Candidate
	Value float64
}

// Solve returns the value-maximizing squad satisfying c's budget,
// per-position quota, per-club cap, and (if set) locked/change-count
// constraints. Returns an error if no feasible squad exists — too few
// candidates in some position, or the budget can't stretch to fill every
// quota even with the cheapest available candidates.
func Solve(candidates []Candidate, c SquadConstraints) (Result, error) {
	if err := validateConstraints(c); err != nil {
		return Result{}, err
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
			return Result{}, fmt.Errorf("position %d: only %d candidates available, need %d", pos, len(pruned[pos]), c.PositionQuota[pos])
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
	for _, pos := range [...]int{1, 4, 3, 2} {
		pool = append(pool, byPositionSorted[pos]...)
	}

	// One knapsack-exact-count DP table per position, built once here and
	// queried (O(1)) by bound() at every node — see buildPositionDP's doc
	// comment for what it computes.
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
	var dp [5]positionDP
	totalMaxCost := 0
	for pos := 1; pos <= numPositions; pos++ {
		posMax := positionMaxCost(byPositionSorted[pos], c.PositionQuota[pos])
		totalMaxCost += posMax
		tableBudget := c.BudgetTenths
		if posMax < tableBudget {
			tableBudget = posMax
		}
		dp[pos] = buildPositionDP(byPositionSorted[pos], c.PositionQuota[pos], tableBudget)
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
			row[b] = dp[pos].bestValue(c.PositionQuota[pos], b)
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

	s := &solver{
		pool: pool, dp: dp, mergeCap: mergeCap,
		suffixAfter: suffixAfter, blockIndexOf: blockIndexOf,
		locked: locked, c: c, best: Result{Value: negInf},
	}
	s.recurse(0, nil, 0, c.PositionQuota, map[int]int{}, 0)

	if s.best.Value == negInf {
		return Result{}, fmt.Errorf("no feasible squad found under the given budget and constraints")
	}

	// Deterministic output ordering — the search itself doesn't guarantee
	// one, and callers (golden fixtures in particular) need stability.
	sort.Slice(s.best.Squad, func(i, j int) bool {
		if s.best.Squad[i].Position != s.best.Squad[j].Position {
			return s.best.Squad[i].Position < s.best.Squad[j].Position
		}
		return s.best.Squad[i].ID < s.best.Squad[j].ID
	})
	return s.best, nil
}

const negInf = -(1 << 62) // a value no real squad total can reach, used as "no result yet"

type solver struct {
	pool         []Candidate // branch order: position blocks, value-descending within each
	dp           [5]positionDP
	mergeCap     int                        // common budget range suffixAfter's rows are sized to
	suffixAfter  [numPositions][]float64    // suffixAfter[i]: best combined value of every position after blockOrder[i], full quota, sharing budget
	blockIndexOf [5]int                     // position -> its index in the fixed branch order
	locked       map[int]bool
	c            SquadConstraints
	best         Result
}

// recurse makes one include/exclude decision on pool[idx] per call — a
// standard depth-first branch-and-bound over a fixed item order. chosen,
// spent, posLeft, clubCount, and changesUsed together describe the partial
// solution up to (not including) idx.
func (s *solver) recurse(idx int, chosen []Candidate, spent int, posLeft [5]int, clubCount map[int]int, changesUsed int) {
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

	if bound(s.dp, s.suffixAfter, s.blockIndexOf, s.mergeCap, cnd.Position, posLeft, s.c.BudgetTenths-spent, currentValue) <= s.best.Value {
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
func bound(dp [5]positionDP, suffixAfter [numPositions][]float64, blockIndexOf [5]int, mergeCap int, currentPos int, posLeft [5]int, budgetLeft int, currentValue float64) float64 {
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
		cv := dp[currentPos].bestValue(posLeft[currentPos], budgetLeft-b2)
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

// positionDP answers, for one position's candidate list: what's the best
// total value from choosing exactly k of them with total cost ≤ b? Built
// once per Solve call (O(candidates × quota × budget)), queried in O(1)
// after that.
type positionDP struct {
	maxBudget int
	// table[k][b] = best value for exactly k candidates costing <= b.
	// table[0][*] = 0. An entry that's still unreachableValue means no
	// combination of k candidates fits within b.
	table [][]float64
}

const unreachableValue = -1e18

// buildPositionDP runs a standard 0/1 "exact count" knapsack DP over
// candidates (already this position's pruned, deduplicated list), then
// converts each row from "cost exactly b" to "cost at most b" via a prefix
// max, so bestValue can look up any budget directly.
func buildPositionDP(candidates []Candidate, quota int, maxBudget int) positionDP {
	if maxBudget < 0 {
		maxBudget = 0
	}
	table := make([][]float64, quota+1)
	for k := range table {
		table[k] = make([]float64, maxBudget+1)
		if k > 0 {
			for b := range table[k] {
				table[k][b] = unreachableValue
			}
		}
	}
	for _, cnd := range candidates {
		price := cnd.PriceTenths
		if price > maxBudget {
			continue // can never be afforded regardless of what else is picked
		}
		for k := quota; k >= 1; k-- {
			for b := maxBudget; b >= price; b-- {
				prev := table[k-1][b-price]
				if prev == unreachableValue {
					continue
				}
				if v := prev + cnd.Value; v > table[k][b] {
					table[k][b] = v
				}
			}
		}
	}
	for k := range table {
		for b := 1; b <= maxBudget; b++ {
			if table[k][b-1] > table[k][b] {
				table[k][b] = table[k][b-1]
			}
		}
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
