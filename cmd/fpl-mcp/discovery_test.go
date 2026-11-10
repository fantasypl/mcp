package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func intp(v int) *int { return &v }

func TestBuildUpcomingFixtures(t *testing.T) {
	b := &fpl.Bootstrap{
		Teams:  []fpl.Team{{ID: 1, ShortName: "ARS"}, {ID: 2, ShortName: "AVL"}},
		Events: []fpl.Event{{ID: 3, IsCurrent: true, Finished: true}, {ID: 4, IsNext: true}},
	}
	fixtures := []fpl.Fixture{
		{Event: intp(3), TeamH: 1, TeamA: 2},                                      // already played
		{Event: intp(9), TeamH: 1, TeamA: 2},                                      // past the window
		{Event: nil, TeamH: 1, TeamA: 2},                                          // unscheduled
		{Event: intp(8), TeamH: 2, TeamA: 1, KickoffTime: "2026-10-01T14:00:00Z"}, // last in window
		{Event: intp(4), TeamH: 1, TeamA: 2, TeamHDifficulty: 2, TeamADifficulty: 4, KickoffTime: "2026-09-01T19:00:00Z"},
		{Event: intp(4), TeamH: 2, TeamA: 1, KickoffTime: "2026-09-01T14:00:00Z"},
	}
	got := buildUpcomingFixtures(b, fixtures)

	if got.FromGameweek != 4 || got.ToGameweek != 8 {
		t.Fatalf("window %d-%d, want 4-8", got.FromGameweek, got.ToGameweek)
	}
	if len(got.Fixtures) != 3 {
		t.Fatalf("got %d fixtures, want 3: %+v", len(got.Fixtures), got.Fixtures)
	}
	// Sorted by gameweek, then kickoff.
	if got.Fixtures[0].KickoffTime != "2026-09-01T14:00:00Z" || got.Fixtures[2].Gameweek != 8 {
		t.Errorf("order = %+v", got.Fixtures)
	}
	f := got.Fixtures[1]
	if f.Home != "ARS" || f.Away != "AVL" || f.HomeFDR != 2 || f.AwayFDR != 4 {
		t.Errorf("fixture = %+v", f)
	}
}

func TestBuildTopPlayers(t *testing.T) {
	b := &fpl.Bootstrap{Teams: []fpl.Team{{ID: 1, ShortName: "ARS"}}}
	for id := 1; id <= 60; id++ {
		b.Elements = append(b.Elements, fpl.Player{ID: id, Team: 1, ElementType: 3, WebName: "P", Form: fpl.Num(id % 7), TotalPoints: id})
	}
	got := buildTopPlayers(b)

	if len(got.Players) != topPlayersCount {
		t.Fatalf("got %d players, want %d", len(got.Players), topPlayersCount)
	}
	for i := 1; i < len(got.Players); i++ {
		a, c := got.Players[i-1], got.Players[i]
		if a.Form < c.Form || (a.Form == c.Form && a.TotalPoints < c.TotalPoints) {
			t.Fatalf("not ranked by form then points at %d: %+v then %+v", i, a, c)
		}
	}
	// Form 6 is the top; among those, id 55 has the most points.
	if top := got.Players[0]; top.ID != 55 || top.Form != 6 || top.Team != "ARS" || top.Position != "MID" || top.Status != "a" {
		t.Errorf("top = %+v", top)
	}
}

// Both resources read through a real server against the committed
// midseason fixtures.
func TestBrowsingResources(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bootstrap-static/":
			http.ServeFile(w, r, "../../testdata/bootstrap_midseason.json")
		case "/fixtures/":
			http.ServeFile(w, r, "../../testdata/fixtures.json")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := fpl.NewClient()
	client.BaseURL = srv.URL

	ctx := context.Background()
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	addBrowsingResources(s, client)
	t1, t2 := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	read := func(uri string, into any) {
		t.Helper()
		res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		if err := json.Unmarshal([]byte(res.Contents[0].Text), into); err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
	}

	var fx upcomingFixtures
	read(upcomingFixturesURI, &fx)
	if fx.ToGameweek-fx.FromGameweek != upcomingGameweeks-1 || len(fx.Fixtures) == 0 {
		t.Errorf("fixtures/upcoming = gameweeks %d-%d with %d fixtures", fx.FromGameweek, fx.ToGameweek, len(fx.Fixtures))
	}
	for _, f := range fx.Fixtures {
		if f.Home == "" || f.Away == "" || f.HomeFDR < 1 || f.HomeFDR > 5 {
			t.Errorf("bad fixture row %+v", f)
		}
	}

	var top topPlayers
	read(topPlayersURI, &top)
	if len(top.Players) != topPlayersCount || top.Players[0].Name == "" {
		t.Errorf("players/top has %d players, first %+v", len(top.Players), top.Players[0])
	}
}
