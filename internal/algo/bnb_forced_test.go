package algo

import (
	"math"
	"math/rand"
	"testing"
)

// Forced candidates (#20) must always be in the squad; Solve is checked
// against bruteForceSolve, which skips any squad missing one.
func TestBnBForcedMatchesBruteForce(t *testing.T) {
	cases := []struct {
		name   string
		split  [5]int
		quota  [5]int
		lineup *LineupRules
		clubs  int
	}{
		{"plain sum", [5]int{0, 6, 8, 8, 6}, [5]int{0, 2, 3, 3, 2}, nil, 6},
		{"FPL lineup", [5]int{0, 3, 7, 7, 5}, fplQuota, &fplLineup, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rng := rand.New(rand.NewSource(20))
			ran := 0
			for trial := 0; trial < 15; trial++ {
				candidates := randomCandidates(rng, tc.split, tc.clubs)
				// Force two or three random candidates, which the unforced
				// optimum often leaves out.
				var forced []int
				for _, i := range rng.Perm(len(candidates))[:2+rng.Intn(2)] {
					forced = append(forced, candidates[i].ID)
				}
				c := SquadConstraints{BudgetTenths: 1 << 30, PositionQuota: tc.quota, MaxPerClub: 3, MaxChanges: -1, Forced: forced, Lineup: tc.lineup}
				proof, err := Solve(candidates, c)
				if err != nil {
					continue // this forced set breaks a quota or club cap; covered below
				}
				minCost := 0
				for _, cnd := range proof.Squad {
					minCost += cnd.PriceTenths
				}
				c.BudgetTenths = minCost + rng.Intn(200)

				got, err := Solve(candidates, c)
				if err != nil {
					t.Fatalf("trial %d: Solve: %v", trial, err)
				}
				assertValidSquad(t, got.Squad, c)
				in := map[int]bool{}
				for _, cnd := range got.Squad {
					in[cnd.ID] = true
				}
				for _, id := range forced {
					if !in[id] {
						t.Fatalf("trial %d: forced candidate %d missing from the squad", trial, id)
					}
				}
				want := bruteForceSolve(candidates, c)
				if math.Abs(got.Value-want.Value) > 1e-6 {
					t.Fatalf("trial %d: Solve value = %v, brute force = %v", trial, got.Value, want.Value)
				}
				ran++
			}
			if ran < 10 {
				t.Fatalf("only %d of 15 trials had a feasible forced set", ran)
			}
		})
	}
}

func TestSolveRejectsImpossibleForcedSets(t *testing.T) {
	cands := []Candidate{
		{ID: 1, Position: 1, PriceTenths: 50, Club: 1, Value: 1},
		{ID: 2, Position: 1, PriceTenths: 50, Club: 1, Value: 1},
		{ID: 3, Position: 2, PriceTenths: 50, Club: 1, Value: 1},
		{ID: 4, Position: 2, PriceTenths: 50, Club: 1, Value: 1},
		{ID: 5, Position: 2, PriceTenths: 50, Club: 2, Value: 1},
	}
	base := SquadConstraints{BudgetTenths: 1000, PositionQuota: [5]int{0, 1, 2, 0, 0}, MaxPerClub: 3, MaxChanges: -1}
	for name, tc := range map[string]struct {
		forced []int
		edit   func(*SquadConstraints)
	}{
		"too many in a position": {[]int{1, 2}, nil},
		"over the club cap":      {[]int{1, 3, 4}, func(c *SquadConstraints) { c.MaxPerClub = 2 }},
		"over budget":            {[]int{1, 3}, func(c *SquadConstraints) { c.BudgetTenths = 90 }},
		"unknown id":             {[]int{99}, nil},
		"listed twice":           {[]int{3, 3}, nil},
		"cannot fill the rest":   {[]int{1, 3}, func(c *SquadConstraints) { c.BudgetTenths = 120 }},
	} {
		c := base
		c.Forced = tc.forced
		if tc.edit != nil {
			tc.edit(&c)
		}
		if _, err := Solve(cands, c); err == nil {
			t.Errorf("%s: want an error, got nil", name)
		}
	}
}
