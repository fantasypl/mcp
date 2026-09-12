package algo

import (
	"context"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/golden"
)

// newCandidatePlayer returns a minimal, schema-valid player for
// buildCandidates tests, which only look at ID, ElementType, Team, NowCost,
// and Status/availability — not the full scoring surface newPlayer() covers.
func newCandidatePlayer(id, elementType int, value float64) fpl.Player {
	return fpl.Player{
		ID:            id,
		ElementType:   elementType,
		Team:          1,
		Status:        "a",
		NowCost:       50,
		Form:          numOf(value),
		PointsPerGame: numOf(value),
	}
}

func TestBuildCandidatesExcludesUnselectableUnlessAlwaysIncluded(t *testing.T) {
	p := newCandidatePlayer(1, 3, 5.0)
	p.Status = "u"

	got := buildCandidates([]fpl.Player{p}, nil, nil, nil)
	if len(got) != 0 {
		t.Fatalf("status=u player should be excluded by default, got %d candidates", len(got))
	}

	got = buildCandidates([]fpl.Player{p}, nil, nil, map[int]bool{1: true})
	if len(got) != 1 {
		t.Fatalf("status=u player in alwaysInclude should survive, got %d candidates", len(got))
	}
}

func TestBuildCandidatesExplicitExcludeWinsOverAlwaysInclude(t *testing.T) {
	p := newCandidatePlayer(1, 3, 5.0)

	got := buildCandidates([]fpl.Player{p}, nil, map[int]bool{1: true}, map[int]bool{1: true})
	if len(got) != 0 {
		t.Fatalf("explicit exclude should win over alwaysInclude, got %d candidates", len(got))
	}
}

func TestBuildCandidatesPoolCapKeepsTopKByValue(t *testing.T) {
	poolCap := candidatePoolCap[3] // MID
	elements := make([]fpl.Player, 0, poolCap+5)
	for i := 0; i < poolCap+5; i++ {
		// Descending value: ID 0 is the best, ID poolCap+4 is the worst.
		elements = append(elements, newCandidatePlayer(i, 3, float64(poolCap+5-i)))
	}

	got := buildCandidates(elements, nil, nil, nil)
	if len(got) != poolCap {
		t.Fatalf("got %d candidates, want exactly the pool cap of %d", len(got), poolCap)
	}
	for _, c := range got {
		if c.ID >= poolCap {
			t.Errorf("candidate %d should have been trimmed by the pool cap (best %d survive)", c.ID, poolCap)
		}
	}
}

func TestBuildCandidatesAlwaysIncludeBypassesPoolCap(t *testing.T) {
	poolCap := candidatePoolCap[3]
	elements := make([]fpl.Player, 0, poolCap+1)
	for i := 0; i < poolCap; i++ {
		elements = append(elements, newCandidatePlayer(i, 3, float64(poolCap-i)+10)) // all outrank the low-value one below
	}
	lowValueID := poolCap
	elements = append(elements, newCandidatePlayer(lowValueID, 3, 0.01))

	got := buildCandidates(elements, nil, nil, map[int]bool{lowValueID: true})

	found := false
	for _, c := range got {
		if c.ID == lowValueID {
			found = true
		}
	}
	if !found {
		t.Fatal("alwaysInclude candidate should survive the pool cap even ranked last")
	}
	if len(got) != poolCap+1 {
		t.Fatalf("got %d candidates, want pool cap (%d) plus the always-included one", len(got), poolCap)
	}
}

func TestBuildCandidatesValueComesFromProjectExpectedPoints(t *testing.T) {
	p := newCandidatePlayer(1, 3, 5.0)
	window := map[int][]projectionFixture{1: {{Gameweek: 1, FDR: 3, IsHome: true}}}

	got := buildCandidates([]fpl.Player{p}, window, nil, nil)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	want := projectExpectedPoints(&p, window[1])
	if got[0].Value != want {
		t.Errorf("Candidate.Value = %v, want projectExpectedPoints result %v", got[0].Value, want)
	}
}

func newEngineForOptimalSquad(t *testing.T, fixture string) *Engine {
	t.Helper()
	c := &StubClient{
		bootstrap: loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_"+fixture+".json")),
		fixtures:  loadJSON[[]fpl.Fixture](t, testdataPath("fixtures.json")),
	}
	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }
	return e
}

func TestOptimalSquadMatchesGolden(t *testing.T) {
	bothFixtures(t, func(t *testing.T, e *Engine, suffix string) {
		got, err := e.OptimalSquad(context.Background(), 1000, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		golden.Assert(t, goldenPath("optimal_squad"+suffix), got)
	})
}

func TestOptimalSquadRespectsConstraints(t *testing.T) {
	e := newEngineForOptimalSquad(t, "midseason")
	got, err := e.OptimalSquad(context.Background(), 1000, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Squad) != 15 {
		t.Fatalf("squad has %d players, want 15", len(got.Squad))
	}
	if got.TotalCostM > 100.0+1e-9 {
		t.Errorf("total cost %.1f exceeds the 100.0m budget", got.TotalCostM)
	}

	byPos := map[string]int{}
	byTeam := map[string]int{}
	for _, s := range got.Squad {
		byPos[s.Position]++
		byTeam[s.Team]++
	}
	wantPos := map[string]int{"GKP": 2, "DEF": 5, "MID": 5, "FWD": 3}
	for pos, want := range wantPos {
		if byPos[pos] != want {
			t.Errorf("position %s: got %d, want %d", pos, byPos[pos], want)
		}
	}
	for team, n := range byTeam {
		if n > 3 {
			t.Errorf("team %s has %d players, want at most 3", team, n)
		}
	}
}

func TestOptimalSquadExcludesRequestedPlayers(t *testing.T) {
	e := newEngineForOptimalSquad(t, "midseason")
	baseline, err := e.OptimalSquad(context.Background(), 1000, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	excludeID := baseline.Squad[0].ID

	got, err := e.OptimalSquad(context.Background(), 1000, nil, []int{excludeID})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got.Squad {
		if s.ID == excludeID {
			t.Errorf("excluded player %d appeared in the squad", excludeID)
		}
	}
}

// A realistic-scale timing check through the full pipeline (translation,
// pool-cap prefilter, Solve) — bnb_test.go already proves the kernel itself
// is tractable at this scale; this confirms the pipeline built on top of it
// is too.
func TestOptimalSquadRealisticScaleTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping realistic-scale timing check in -short mode")
	}
	e := newEngineForOptimalSquad(t, "midseason")

	start := time.Now()
	got, err := e.OptimalSquad(context.Background(), 1000, nil, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > optimalSquadTimeLimit+2*time.Second {
		t.Errorf("OptimalSquad took %v, want well under %v", elapsed, optimalSquadTimeLimit+2*time.Second)
	}
	t.Logf("elapsed=%v optimal=%v projected_points=%.2f", elapsed, got.Optimal, got.ProjectedPoints)
}
