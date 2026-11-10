package algo

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Issue #51: pick the XI, bench order and captain for an existing 15.
//
// The case study: in GW10 a form-ranked XI benched Gross (6.7 points per
// game, on penalties and set pieces) and Vuskovic, and started Neto, Manzambi
// and Van Hecke. The synthetic squad (picks_squad1.json) has the same shape
// when its two Brighton midfielders on the bench, Howell and Oriola, are
// given Gross-like seasons and two starting midfielders, B.Fernandes and
// Enzo, are given hot form but weak seasons.
const (
	howellID      = 132 // bench slot 13, BHA MID
	oriolaID      = 134 // bench slot 14, BHA MID
	bFernandesID  = 426 // starter, MUN MID
	enzoID        = 155 // starter, CHE MID
	pickfordID    = 226 // starting GK
	forsterID     = 58  // bench GK
	pickXITeamGW1 = 1
)

func pickXIEngine(t *testing.T) *Engine {
	t.Helper()
	e := hubEngine(t)
	stub := e.client.(*StubClient)
	for i := range stub.bootstrap.Elements {
		p := &stub.bootstrap.Elements[i]
		switch p.ID {
		case howellID, oriolaID:
			// Steady, regular, on set pieces, cold for a fortnight.
			p.PointsPerGame, p.Form, p.EPNext = numOf(6.7), numOf(3.2), numOf(3.2)
			p.Starts, p.Status = 1, "a"
			p.PenaltiesOrder = ptr(1)
		case bFernandesID, enzoID:
			// Hot fortnight, weak season: the weakest outfield players in
			// the squad on season output, the strongest on form.
			p.PointsPerGame, p.Form, p.EPNext = numOf(0.3), numOf(9.0), numOf(9.0)
			p.Starts, p.Status = 1, "a"
		}
	}
	return e
}

func TestPickXIStartsSeasonOutputOverForm(t *testing.T) {
	e := pickXIEngine(t)
	got, err := e.PickXI(context.Background(), syntheticTeamID, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*PickXIResult)

	starters := map[int]bool{}
	for _, p := range res.StartingXI {
		starters[p.ID] = true
	}
	if len(res.StartingXI) != 11 || len(res.Bench) != 4 {
		t.Fatalf("XI %d, bench %d; want 11 and 4", len(res.StartingXI), len(res.Bench))
	}
	for _, id := range []int{howellID, oriolaID} {
		if !starters[id] {
			t.Errorf("player %d (6.7 points per game, cold form) is benched", id)
		}
	}
	for _, id := range []int{bFernandesID, enzoID} {
		if starters[id] {
			t.Errorf("player %d (0.3 points per game, hot form) starts", id)
		}
	}
	if res.Bench[0].Position != "GKP" || res.Bench[0].Slot != 12 {
		t.Errorf("bench[0] = %+v, want the spare goalkeeper in slot 12", res.Bench[0])
	}

	// The moves from the manager's own XI are listed.
	if !slices.Contains(res.ChangesFromYourXI.In, "Howell") || !slices.Contains(res.ChangesFromYourXI.Out, "B.Fernandes") {
		t.Errorf("changes = %+v, want Howell in and B.Fernandes out", res.ChangesFromYourXI)
	}
}

func TestPickXIShowsComponentScores(t *testing.T) {
	e := pickXIEngine(t)
	got, err := e.PickXI(context.Background(), syntheticTeamID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var howell *PickXIPlayer
	for _, p := range append(got.(*PickXIResult).StartingXI, got.(*PickXIResult).Bench...) {
		if p.ID == howellID {
			howell = &p
		}
	}
	if howell == nil {
		t.Fatal("Howell missing from the result")
	}
	c := howell.Components
	if c.PointsPerGame != 6.7 || c.StartsShare != 1 || c.Form != 3.2 || !slices.Contains(c.SetPieces, "penalties") {
		t.Errorf("components = %+v, want ppg 6.7, starts share 1, form 3.2, on penalties", c)
	}
	if c.SetPieceBonus <= 0 || howell.PickScore != Round(c.ProjectedPoints+c.SetPieceBonus, 2) {
		t.Errorf("pick_score %.2f, want projected_points %.2f + set_piece_bonus %.2f", howell.PickScore, c.ProjectedPoints, c.SetPieceBonus)
	}
	if c.CaptainScore == 0 || c.Venue == "" {
		t.Errorf("components = %+v, want captain_score and venue", c)
	}
}

func TestPickXIAlternativeFormations(t *testing.T) {
	e := pickXIEngine(t)
	got, err := e.PickXI(context.Background(), syntheticTeamID, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*PickXIResult)
	if len(res.AlternativeFormations) != 2 {
		t.Fatalf("got %d alternatives, want the two nearest", len(res.AlternativeFormations))
	}
	prev := 0.0
	for _, alt := range res.AlternativeFormations {
		if alt.Formation == res.Formation {
			t.Errorf("alternative repeats the chosen formation %s", alt.Formation)
		}
		if alt.PointsDifference > 0 || alt.PointsDifference > prev {
			t.Errorf("alternatives = %+v, want non-positive differences, nearest first", res.AlternativeFormations)
		}
		prev = alt.PointsDifference
		if len(alt.Swaps) == 0 {
			t.Errorf("alternative %s lists no swaps", alt.Formation)
		}
	}
}

func TestPickXICaptainFromTheXI(t *testing.T) {
	e := pickXIEngine(t)
	got, err := e.PickXI(context.Background(), syntheticTeamID, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*PickXIResult)
	inXI := func(id int) bool {
		for _, p := range res.StartingXI {
			if p.ID == id {
				return true
			}
		}
		return false
	}
	if res.Captain == nil || res.ViceCaptain == nil || !inXI(res.Captain.ID) || !inXI(res.ViceCaptain.ID) || res.Captain.ID == res.ViceCaptain.ID {
		t.Fatalf("captain %+v, vice %+v: want two different starters", res.Captain, res.ViceCaptain)
	}
	if res.Captain.Reason == "" {
		t.Error("captain has no reason")
	}
}

// Benching two of three players from one match is a bet against that match,
// and must be reported as one.
func TestFixtureExposureFlagsBenchedStacks(t *testing.T) {
	mk := func(id int, name string) PickXIPlayer {
		return PickXIPlayer{ID: id, Name: name, Team: "BHA", Opponent: "BRE(H)"}
	}
	xi := []PickXIPlayer{mk(1, "De Cuyper"), {ID: 4, Name: "Haaland", Team: "MCI", Opponent: "NFO(A)"}}
	bench := []PickXIPlayer{mk(2, "Gross"), mk(3, "Vuskovic")}

	got := fixtureExposure(xi, bench)
	var bha *FixtureExposure
	for i := range got {
		if got[i].Fixture == "BHA v BRE (H)" {
			bha = &got[i]
		}
	}
	if bha == nil {
		t.Fatalf("exposure = %+v, want a BHA v BRE (H) group", got)
	}
	if bha.InXI != 1 || bha.Benched != 2 || !strings.Contains(bha.Note, "bet against") {
		t.Errorf("BHA group = %+v, want 1 in the XI, 2 benched, and a note", *bha)
	}
	for _, g := range got {
		if g.Fixture == "MCI v NFO (A)" {
			t.Errorf("a one-player fixture is not a stack: %+v", g)
		}
	}
}

// Equal pick scores are broken by form, the only place form enters.
func TestPickXIFormIsOnlyATiebreak(t *testing.T) {
	a := PickXIPlayer{ID: 1, PickScore: 5, Components: PickXIComponents{Form: 2}}
	b := PickXIPlayer{ID: 2, PickScore: 5, Components: PickXIComponents{Form: 8}}
	c := PickXIPlayer{ID: 3, PickScore: 5.01, Components: PickXIComponents{Form: 0}}
	players := []PickXIPlayer{a, b, c}
	slices.SortStableFunc(players, comparePickXI)
	if players[0].ID != 3 || players[1].ID != 2 || players[2].ID != 1 {
		t.Errorf("order = %d %d %d, want 3 (higher score), then 2 (form breaks the tie), then 1",
			players[0].ID, players[1].ID, players[2].ID)
	}
}

func TestPickXIUnknownTeam(t *testing.T) {
	e := NewEngine(NewStubClient(loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_midseason.json")), nil))
	got, err := e.PickXI(context.Background(), 4242, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := got.(*PickXIError); !ok || !strings.Contains(r.Error, "4242") {
		t.Errorf("got %#v, want a PickXIError naming the team", got)
	}
}
