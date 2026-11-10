package main

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/algo"
	"github.com/fantasypl/mcp/internal/config"
	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/fplauth"
)

// fakeLogin is a logged-in account whose team is entry.
type fakeLogin struct {
	algo.MyTeamSource
	entry int
}

func (f fakeLogin) MyEntry(context.Context) (int, error) { return f.entry, nil }

func TestDefaultsTeam(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		d       defaults
		in      int
		want    int
		wantErr bool
	}{
		{"given", defaults{saved: config.Saved{TeamID: 7}}, 42, 42, false},
		{"given but invalid", defaults{}, -1, -1, true},
		{"saved", defaults{saved: config.Saved{TeamID: 7}}, 0, 7, false},
		{"saved beats login", defaults{saved: config.Saved{TeamID: 7}, login: fakeLogin{entry: 9}}, 0, 7, false},
		{"login", defaults{login: fakeLogin{entry: 9}}, 0, 9, false},
		{"nothing", defaults{}, 0, 0, true},
	}
	for _, tc := range tests {
		got, e := tc.d.team(ctx, tc.in)
		if got != tc.want || (e != "") != tc.wantErr {
			t.Errorf("%s: team(%d) = %d, %q; want %d, error %v", tc.name, tc.in, got, e, tc.want, tc.wantErr)
		}
	}
}

func TestDefaultsLeague(t *testing.T) {
	d := defaults{saved: config.Saved{Leagues: []config.SavedLeague{{ID: 314159}, {ID: 271828}}}}
	if id, e := d.league(0); id != 314159 || e != "" {
		t.Errorf("league(0) = %d, %q; want the first saved league", id, e)
	}
	if id, e := d.league(5); id != 5 || e != "" {
		t.Errorf("league(5) = %d, %q; want 5", id, e)
	}
	if _, e := (&defaults{}).league(0); !strings.Contains(e, "FPL_MCP_LEAGUES") {
		t.Errorf("with nothing saved, error %q should say how to save leagues", e)
	}
}

func TestDefaultsInstructions(t *testing.T) {
	if got := (&defaults{}).instructions(); got != "" {
		t.Errorf("nothing saved: instructions = %q, want none", got)
	}
	d := defaults{saved: config.Saved{TeamID: 2233768, Leagues: []config.SavedLeague{{ID: 314159, Label: "Work"}, {ID: 271828}}}}
	got := d.instructions()
	for _, want := range []string{"2233768", "314159 (Work), 271828", "league_ownership"} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions %q lack %q", got, want)
		}
	}
}

// loadDefaults hands the saved leagues to the engine, in order.
func TestLoadDefaults(t *testing.T) {
	t.Setenv("FPL_MCP_TEAM_ID", "2233768")
	t.Setenv("FPL_MCP_LEAGUES", "314159:Work, 271828")
	engine := algo.NewEngine(fpl.NewClient())
	d := loadDefaults(engine)
	if d.saved.TeamID != 2233768 || len(engine.Leagues) != 2 || engine.Leagues[0] != (algo.LeagueRef{ID: 314159, Label: "Work"}) {
		t.Errorf("saved %+v, engine leagues %+v", d.saved, engine.Leagues)
	}
}

// A saved team ID that could not be read is named in the error, so a typo
// is not mistaken for a blank setting.
func TestDefaultsReportSettingProblems(t *testing.T) {
	t.Setenv("FPL_MCP_TEAM_ID", "2233768x")
	t.Setenv("FPL_MCP_LEAGUES", "abc")
	d := loadDefaults(algo.NewEngine(fpl.NewClient()))
	if _, e := d.team(context.Background(), 0); !strings.Contains(e, "2233768x") {
		t.Errorf("team error %q should quote the bad setting", e)
	}
	if _, e := d.league(0); !strings.Contains(e, "abc") {
		t.Errorf("league error %q should quote the bad setting", e)
	}
}

// With nothing saved, a tool called without team_id says how to save one,
// and never reaches the FPL API.
func TestToolsWithoutSavedTeam(t *testing.T) {
	t.Setenv(fplauth.EnvLogin, "")
	for _, tool := range []string{"fpl_manager_hub", "pick_xi", "transfer_suggestions", "optimal_transfers", "live_points", "manager_history", "manager_transfer_history", "chip_strategy", "squad_scout"} {
		got := callTool(t, tool, map[string]any{})
		if !isError(got) || !strings.Contains(got["error"].(string), "FPL_MCP_TEAM_ID") {
			t.Errorf("%s without team_id = %v, want an error naming FPL_MCP_TEAM_ID", tool, got)
		}
	}
	for _, tool := range []string{"rival_tracker", "league_analyzer"} {
		got := callTool(t, tool, map[string]any{"team_id": 1})
		if !isError(got) || !strings.Contains(got["error"].(string), "FPL_MCP_LEAGUES") {
			t.Errorf("%s without league_id = %v, want an error naming FPL_MCP_LEAGUES", tool, got)
		}
	}
}

// The README's example setting must parse, so the docs and the parser
// cannot drift apart.
func TestReadmeLeaguesExampleParses(t *testing.T) {
	raw, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?s)<!-- fpl-leagues-example -->\\s*```text\\s*(.*?)\\s*```").FindSubmatch(raw)
	if m == nil {
		t.Fatal("README has no fpl-leagues-example block")
	}
	leagues, err := config.ParseLeagues(string(m[1]))
	if err != nil || len(leagues) < 2 || leagues[1].Label == "" {
		t.Errorf("README example parses to %+v, %v; want at least two leagues, the second labelled", leagues, err)
	}
}
