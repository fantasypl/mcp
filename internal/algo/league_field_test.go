package algo

import (
	"context"
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// The rivals scenario: the caller is 999002 (rank 2) in league 12345 with
// 999001 (rank 1, captained 426), 999003 (rank 3, captained 1) and 999004,
// whose squad is missing. 999002 captained 384, which no rival owns.
func savedLeagueEngine(t *testing.T, leagues ...LeagueRef) *Engine {
	t.Helper()
	e := chosenRivalsEngine(t)
	e.Leagues = leagues
	return e
}

func TestLeagueFieldCountsRivals(t *testing.T) {
	e := savedLeagueEngine(t, LeagueRef{ID: leagueScenarioID, Label: "Mates"})
	f := e.LeagueField(context.Background(), rivalsUserTeamID)
	if f == nil || len(f.Leagues) != 1 {
		t.Fatalf("field = %+v, want one league", f)
	}
	fl := f.Leagues[0]
	if fl.Name != "The Bootleg Bin" || fl.You == nil || fl.You.Rank != 2 {
		t.Errorf("league = %q, you = %+v; want The Bootleg Bin with the caller at rank 2", fl.Name, fl.You)
	}
	if f.Missing != 1 || fl.tally.rivals != 2 {
		t.Errorf("rivals = %d, missing = %d; want 2 read and 999004 missing", fl.tally.rivals, f.Missing)
	}

	tests := []struct {
		id               int
		owned, captained int
		eo               float64
		stance           string
	}{
		{426, 1, 1, 100, StanceShield},     // 999001's captain: 1 of 2 captained
		{4, 1, 0, 50, StanceTemplate},      // 999001's starter: half own him
		{58, 1, 0, 0, StanceTemplate},      // 999001's bench: owned, but no multiplier
		{384, 0, 0, 0, StanceDifferential}, // the caller's own captain
	}
	for _, tc := range tests {
		o := f.Ownership(tc.id)
		if o.OwnedBy != tc.owned || o.CaptainedBy != tc.captained || o.EffectiveOwnershipPct != tc.eo || o.Stance != tc.stance {
			t.Errorf("player %d = %+v, want owned %d, captained %d, EO %.0f, %s", tc.id, o, tc.owned, tc.captained, tc.eo, tc.stance)
		}
	}
	if !strings.Contains(f.Basis(), "gameweek 10") {
		t.Errorf("basis %q should name the gameweek the squads are from", f.Basis())
	}
}

// A rival in two saved leagues counts once overall and once in each league.
func TestLeagueFieldCountsSharedRivalsOnce(t *testing.T) {
	e := savedLeagueEngine(t, LeagueRef{ID: leagueScenarioID}, LeagueRef{ID: 54321})
	stub := e.client.(*StubClient)
	stub.SetLeague(54321, loadJSON[*fpl.LeagueStandings](t, testdataPath("rivals_scenario", "standings.json")))
	f := e.LeagueField(context.Background(), rivalsUserTeamID)
	if f.all.rivals != 2 || f.Leagues[0].tally.rivals != 2 || f.Leagues[1].tally.rivals != 2 {
		t.Errorf("rivals overall %d, per league %d and %d; want 2 each", f.all.rivals, f.Leagues[0].tally.rivals, f.Leagues[1].tally.rivals)
	}
	if o := f.Ownership(426); o.OwnedBy != 1 {
		t.Errorf("426 owned by %d overall, want 1", o.OwnedBy)
	}
}

func TestLeagueFieldBadLeague(t *testing.T) {
	e := savedLeagueEngine(t, LeagueRef{ID: 777})
	f := e.LeagueField(context.Background(), rivalsUserTeamID)
	if f.Basis() != "" || !strings.Contains(f.Leagues[0].Note, "777") {
		t.Errorf("field = %+v, want no ownership and a note naming league 777", f.Leagues[0])
	}
}

func TestPickRivals(t *testing.T) {
	big := make([]fpl.LeagueEntry, 50)
	for i := range big {
		big[i] = fpl.LeagueEntry{Entry: 1000 + i, Rank: i + 1, Total: 2000 - i}
	}
	small := big[:8]

	rivals, you, note := pickRivals(small, false, 1003)
	if len(rivals) != 7 || you == nil || note != "" {
		t.Errorf("small league: %d rivals, you %v, note %q; want everyone else", len(rivals), you, note)
	}
	rivals, _, _ = pickRivals(big, true, 1030)
	if len(rivals) != 2*fieldWindow || rivals[0].Entry != 1020 || rivals[len(rivals)-1].Entry != 1040 {
		t.Errorf("big league: %d rivals from %d; want the %d either side of 1030", len(rivals), rivals[0].Entry, fieldWindow)
	}
	rivals, you, note = pickRivals(big, true, 5)
	if len(rivals) != 2*fieldWindow || you != nil || !strings.Contains(note, "not on the first page") {
		t.Errorf("caller off page: %d rivals, note %q; want the top %d and a note", len(rivals), note, 2*fieldWindow)
	}
}

func TestWithLeagueOwnership(t *testing.T) {
	ctx := context.Background()

	// No saved leagues: the result is unchanged.
	e := savedLeagueEngine(t)
	out, err := e.CaptainPicksForTeam(ctx, rivalsUserTeamID, nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	e.WithLeagueOwnership(ctx, rivalsUserTeamID, out)
	if out.LeagueOwnershipBasis != "" || out.Picks[0].LeagueOwnership != nil {
		t.Error("without saved leagues, captain picks gained league_ownership")
	}

	e = savedLeagueEngine(t, LeagueRef{ID: leagueScenarioID})
	out, err = e.CaptainPicksForTeam(ctx, rivalsUserTeamID, nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	e.WithLeagueOwnership(ctx, rivalsUserTeamID, out)
	if out.LeagueOwnershipBasis == "" {
		t.Error("league_ownership_basis is empty")
	}
	for _, p := range out.Picks {
		if p.LeagueOwnership == nil || p.LeagueOwnership.Rivals != 2 {
			t.Errorf("pick %d league_ownership = %+v, want 2 rivals counted", p.Player.ID, p.LeagueOwnership)
		}
	}
}

func TestManagerHubLeagues(t *testing.T) {
	ctx := context.Background()
	e := savedLeagueEngine(t)
	hub, err := e.ManagerHub(ctx, rivalsUserTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if hub.Leagues != nil || hub.LeagueOwnershipBasis != "" {
		t.Error("without saved leagues, the hub has a leagues section")
	}

	e = savedLeagueEngine(t, LeagueRef{ID: leagueScenarioID, Label: "Mates"})
	hub, err = e.ManagerHub(ctx, rivalsUserTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hub.Leagues) != 1 {
		t.Fatalf("leagues = %d, want 1", len(hub.Leagues))
	}
	l := hub.Leagues[0]
	if l.Label != "Mates" || l.Rank != 2 || l.Managers != 4 || l.RivalsCounted != 2 {
		t.Errorf("league = %+v, want Mates, rank 2 of 4, 2 rivals counted", l)
	}
	if l.PointsBehindLeader == nil || *l.PointsBehindLeader != 8 ||
		l.PointsBehindPlaceAbove == nil || *l.PointsBehindPlaceAbove != 8 ||
		l.PointsAheadOfPlaceBelow == nil || *l.PointsAheadOfPlaceBelow != 152 {
		t.Errorf("gaps = %v, %v, %v; want 8 behind the leader and the place above, 152 ahead of the place below",
			l.PointsBehindLeader, l.PointsBehindPlaceAbove, l.PointsAheadOfPlaceBelow)
	}
	if l.WinProbability == nil {
		t.Error("win_probability is missing")
	}
	// Every rival squad here holds a different 15, so each player they own
	// is held by half of them, and the two captains rank first.
	if len(l.Threats) != hubLeagueLimit || l.Threats[0].ID != 1 || l.Threats[1].ID != 426 {
		t.Errorf("threats = %+v, want %d, led by the rivals' captains 1 and 426", l.Threats, hubLeagueLimit)
	}
	if len(l.Differentials) == 0 || l.Captain == nil || l.Captain.LeagueOwnership == nil {
		t.Errorf("differentials = %+v, captain = %+v; want both set", l.Differentials, l.Captain)
	}
	if !strings.Contains(l.CaptainNote, "rivals captained") {
		t.Errorf("captain_note = %q, want the rivals' most captained player", l.CaptainNote)
	}
	if hub.LeagueOwnershipBasis == "" || hub.Squad[0].LeagueOwnership == nil {
		t.Error("squad players have no league_ownership")
	}

	// The hub's win probability is league_analyzer's.
	la, err := e.AnalyzeLeagueForTeam(ctx, leagueScenarioID, ptr(rivalsUserTeamID))
	if err != nil {
		t.Fatal(err)
	}
	if got := la.(*LeagueAnalysisResult).Caller.WinProbability; got != *l.WinProbability {
		t.Errorf("hub win_probability %v, league_analyzer %v; want the same", *l.WinProbability, got)
	}
}
