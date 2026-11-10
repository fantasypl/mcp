package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseLeagues(t *testing.T) {
	tests := []struct {
		in      string
		want    []SavedLeague
		wantErr string
	}{
		{"", nil, ""},
		{"314159", []SavedLeague{{314159, ""}}, ""},
		{" 314159 , 271828:Work ", []SavedLeague{{314159, ""}, {271828, "Work"}}, ""},
		{"314159; 271828 : Uni friends\n161803", []SavedLeague{{314159, ""}, {271828, "Uni friends"}, {161803, ""}}, ""},
		{"https://fantasy.premierleague.com/leagues/314159/standings/c", []SavedLeague{{314159, ""}}, ""},
		{"314159, 314159:Again", []SavedLeague{{314159, ""}}, ""},
		{"314159, abc, -4", []SavedLeague{{314159, ""}}, "abc, -4"},
		{"1,2,3,4,5,6", []SavedLeague{{1, ""}, {2, ""}, {3, ""}, {4, ""}, {5, ""}}, "only the first 5"},
	}
	for _, tc := range tests {
		got, err := ParseLeagues(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseLeagues(%q) = %v, want %v", tc.in, got, tc.want)
		}
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("ParseLeagues(%q) error %v, want none", tc.in, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("ParseLeagues(%q) error %v, want one containing %q", tc.in, err, tc.wantErr)
		}
	}
}

func TestSavedFromEnv(t *testing.T) {
	t.Setenv("FPL_MCP_TEAM_ID", " 2233768 ")
	t.Setenv("FPL_MCP_LEAGUES", "314159:Work")
	s, errs := SavedFromEnv()
	if len(errs) != 0 || s.TeamID != 2233768 || len(s.Leagues) != 1 || s.Leagues[0].Label != "Work" {
		t.Errorf("got %+v, %v", s, errs)
	}

	// A bad team ID is reported and the leagues still load.
	t.Setenv("FPL_MCP_TEAM_ID", "my team")
	s, errs = SavedFromEnv()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "FPL_MCP_TEAM_ID") || s.TeamID != 0 || len(s.Leagues) != 1 {
		t.Errorf("got %+v, %v; want a FPL_MCP_TEAM_ID error and the league kept", s, errs)
	}
}
