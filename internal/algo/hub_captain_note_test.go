package algo

import (
	"context"
	"strings"
	"testing"
)

// Issue #8 (reworked for #42): the hub shows captain_score and projected_points. When they
// disagree on the best captain among the starters, say so and say why.
func TestCaptainSignalNote(t *testing.T) {
	// The issue's squad, GW5.
	squad := []HubSquadEntry{
		{Slot: 1, Starter: true, Name: "Gibbs-White", CaptainScore: 12.93, ProjectedPoints: 6.5},
		{Slot: 2, Starter: true, Name: "Joao Pedro", CaptainScore: 10.30, ProjectedPoints: 8.2},
		{Slot: 3, Starter: true, Name: "Saka", CaptainScore: 11.50, ProjectedPoints: 7.5},
	}
	note := captainSignalNote(squad)
	for _, want := range []string{"Gibbs-White", "12.9", "Joao Pedro", "8.2", "captain_score", "projected_points", "fixture"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q is missing %q", note, want)
		}
	}

	t.Run("agreement gives no note", func(t *testing.T) {
		agree := []HubSquadEntry{
			{Starter: true, Name: "A", CaptainScore: 12, ProjectedPoints: 8},
			{Starter: true, Name: "B", CaptainScore: 10, ProjectedPoints: 6},
		}
		if got := captainSignalNote(agree); got != "" {
			t.Errorf("got %q, want no note", got)
		}
	})

	t.Run("a tie on projected_points is not a disagreement", func(t *testing.T) {
		tied := []HubSquadEntry{
			{Starter: true, Name: "A", CaptainScore: 12, ProjectedPoints: 6},
			{Starter: true, Name: "B", CaptainScore: 10, ProjectedPoints: 6},
		}
		if got := captainSignalNote(tied); got != "" {
			t.Errorf("got %q, want no note", got)
		}
	})

	t.Run("bench players are not captain candidates", func(t *testing.T) {
		benchLeader := []HubSquadEntry{
			{Starter: true, Name: "A", CaptainScore: 10, ProjectedPoints: 6},
			{Starter: false, Slot: 13, Name: "BenchStar", CaptainScore: 15, ProjectedPoints: 9},
		}
		if got := captainSignalNote(benchLeader); got != "" {
			t.Errorf("got %q, want no note: a benched player can't be captain", got)
		}
	})

	t.Run("a leader with no projection is not a disagreement", func(t *testing.T) {
		newSigning := []HubSquadEntry{
			{Starter: true, Name: "NewSigning", CaptainScore: 12, ProjectedPoints: 0},
			{Starter: true, Name: "Regular", CaptainScore: 10, ProjectedPoints: 6},
		}
		if got := captainSignalNote(newSigning); got != "" {
			t.Errorf("got %q, want no note: the projection is missing, not low", got)
		}
	})

	t.Run("all-zero projections give no note", func(t *testing.T) {
		zero := []HubSquadEntry{
			{Starter: true, Name: "A", CaptainScore: 12},
			{Starter: true, Name: "B", CaptainScore: 10},
		}
		if got := captainSignalNote(zero); got != "" {
			t.Errorf("got %q, want no note", got)
		}
	})
}

// End to end: the note reaches the hub result when the signals disagree.
func TestManagerHubCaptainSignalNoteWired(t *testing.T) {
	e := hubEngine(t)
	stub := e.client.(*StubClient)

	first, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	// Give points per game 15 to the starter with the lowest captain_score
	// and 1 to everyone else, so projected_points must favour a different
	// player. Everyone keeps a projection, so the disagreement is real and not
	// a missing figure. Changing ppg also moves captain_score a little, but
	// not enough to lift the lowest starter to the top.
	var lowest *HubSquadEntry
	for i := range first.Squad {
		s := &first.Squad[i]
		if s.Starter && (lowest == nil || s.CaptainScore < lowest.CaptainScore) {
			lowest = s
		}
	}
	for i := range stub.bootstrap.Elements {
		stub.bootstrap.Elements[i].PointsPerGame = 1
		if stub.bootstrap.Elements[i].ID == lowest.ElementID {
			stub.bootstrap.Elements[i].PointsPerGame = 15
		}
	}

	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.CaptainSignalNote, lowest.Name) {
		t.Errorf("CaptainSignalNote = %q, want it to name %s", got.CaptainSignalNote, lowest.Name)
	}
}
