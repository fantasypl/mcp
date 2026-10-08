package algo

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// Issue #7: captain_pick can rank an explicit list of players, such as a
// squad drafted with optimal_squad, with the same scoring and output shape.
func TestCaptainPicksAmong(t *testing.T) {
	e := hubEngine(t)
	ctx := context.Background()

	all, err := e.CaptainPicks(ctx, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	scoreByID := make(map[int]float64)
	for _, p := range all.Picks {
		scoreByID[p.Player.ID] = p.Score
	}

	// Ranks only the given players, scored exactly as CaptainPicks scores them.
	ids := []int{all.Picks[4].Player.ID, all.Picks[2].Player.ID}
	got, err := e.CaptainPicksAmong(ctx, nil, 0, ids)
	if err != nil {
		t.Fatal(err)
	}
	if got.NumPicks != 2 || len(got.Picks) != 2 {
		t.Fatalf("got %d picks, want 2", got.NumPicks)
	}
	if got.Picks[0].Player.ID != ids[1] || got.Picks[1].Player.ID != ids[0] {
		t.Errorf("order = [%d %d], want [%d %d]", got.Picks[0].Player.ID, got.Picks[1].Player.ID, ids[1], ids[0])
	}
	for _, p := range got.Picks {
		if p.Score != scoreByID[p.Player.ID] {
			t.Errorf("%s score = %v, want %v as in CaptainPicks", p.Player.Name, p.Score, scoreByID[p.Player.ID])
		}
	}
	if got.Gameweek != all.Gameweek || got.AlgorithmVersion != all.AlgorithmVersion {
		t.Errorf("gameweek/version = %d/%s, want %d/%s", got.Gameweek, got.AlgorithmVersion, all.Gameweek, all.AlgorithmVersion)
	}
}

// The max-2-per-club cap is for advice drawn from the whole player pool. A
// caller who names three players from one club gets all three ranked.
func TestCaptainPicksAmongSkipsClubCap(t *testing.T) {
	e := hubEngine(t)
	stub := e.client.(*StubClient)
	byClub := make(map[int][]int)
	for _, p := range stub.bootstrap.Elements {
		byClub[p.Team] = append(byClub[p.Team], p.ID)
	}
	var three []int
	for _, ids := range byClub {
		if len(ids) >= 3 {
			three = ids[:3]
			break
		}
	}
	if three == nil {
		t.Fatal("fixture has no club with three players")
	}

	got, err := e.CaptainPicksAmong(context.Background(), nil, 0, three)
	if err != nil {
		t.Fatal(err)
	}
	if got.NumPicks != 3 {
		t.Errorf("got %d picks from one club, want 3", got.NumPicks)
	}
}

func TestCaptainPicksAmongUnknownID(t *testing.T) {
	e := hubEngine(t)
	_, err := e.CaptainPicksAmong(context.Background(), nil, 0, []int{1, 999_999, 888_888})
	var unknown *UnknownPlayersError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want *UnknownPlayersError", err)
	}
	if !slices.Equal(unknown.IDs, []int{888_888, 999_999}) {
		t.Errorf("IDs = %v, want [888888 999999]", unknown.IDs)
	}
}

// team_id and the same players as player_ids give the same answer.
func TestCaptainPicksForTeamMatchesAmong(t *testing.T) {
	e := hubEngine(t)
	ctx := context.Background()
	stub := e.client.(*StubClient)
	picks, err := stub.TeamPicks(ctx, syntheticTeamID, 1)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int, 0, len(picks.Picks))
	for _, p := range picks.Picks {
		ids = append(ids, p.Element)
	}

	byTeam, err := e.CaptainPicksForTeam(ctx, syntheticTeamID, nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	byIDs, err := e.CaptainPicksAmong(ctx, nil, 5, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(byTeam.Picks) != len(byIDs.Picks) {
		t.Fatalf("team_id gave %d picks, player_ids gave %d", len(byTeam.Picks), len(byIDs.Picks))
	}
	for i := range byTeam.Picks {
		if byTeam.Picks[i].Player.ID != byIDs.Picks[i].Player.ID {
			t.Errorf("pick %d: team_id %d, player_ids %d", i+1, byTeam.Picks[i].Player.ID, byIDs.Picks[i].Player.ID)
		}
	}
}
