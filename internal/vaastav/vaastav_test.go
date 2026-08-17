package vaastav

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

const teamsCSV = `id,code,name,short_name,strength,position,played,points,strength_overall_home,strength_overall_away,strength_attack_home,strength_attack_away,strength_defence_home,strength_defence_away
1,14,Liverpool,LIV,4,1,2,6,1350,1330,1360,1340,1340,1320
2,43,Man City,MCI,5,2,2,4,1400,1380,1410,1390,1390,1370
`

const fixturesCSV = `id,event,team_h,team_a,team_h_difficulty,team_a_difficulty,finished,started,kickoff_time,minutes
1,1,1,2,4,3,True,True,2025-08-09T14:00:00Z,90
2,2,2,1,3,4,True,True,2025-08-16T14:00:00Z,90
3,3,1,2,4,3,False,False,2025-08-23T14:00:00Z,0
`

func gwCSV(rows ...string) string {
	header := "element,name,position,team,value,minutes,total_points,starts,goals_scored,assists,bonus,bps,clean_sheets,yellow_cards,red_cards,expected_goals,expected_assists,expected_goal_involvements,ict_index,influence,creativity,threat\n"
	return header + strings.Join(rows, "\n") + "\n"
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	files := map[string]string{
		"/2099-00/teams.csv":    teamsCSV,
		"/2099-00/fixtures.csv": fixturesCSV,
		"/2099-00/gws/gw1.csv": gwCSV(
			"1,Salah,MID,Liverpool,130,90,10,1,1,1,3,35,1,0,0,0.8,0.5,1.3,15.0,8.0,4.0,3.0",
			"2,Haaland,FWD,Man City,145,90,5,1,1,0,0,20,0,0,0,0.9,0.1,1.0,12.0,5.0,3.0,4.0",
		),
		"/2099-00/gws/gw2.csv": gwCSV(
			"1,Salah,MID,Liverpool,131,90,6,1,0,1,0,22,0,1,0,0.3,0.4,0.7,9.0,4.0,3.0,2.0",
			"2,Haaland,FWD,Man City,146,90,12,1,2,0,3,40,0,0,0,1.5,0.0,1.5,20.0,10.0,2.0,8.0",
		),
		"/2099-00/gws/gw3.csv": gwCSV(
			"1,Salah,MID,Liverpool,131,90,2,1,0,0,0,10,0,0,0,0.1,0.1,0.2,5.0,2.0,1.0,2.0",
			"2,Haaland,FWD,Man City,147,0,0,0,0,0,0,0,0,0,0,0.0,0.0,0.0,0.0,0.0,0.0,0.0",
		),
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
}

func TestBuildCaseReconstructsSeasonToDateState(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	c := NewCorpus(filepath.Join(t.TempDir(), "cache"))
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	got, err := c.BuildCase(context.Background(), "2099-00", 3)
	if err != nil {
		t.Fatalf("BuildCase: %v", err)
	}

	if got.PredictGW != 3 {
		t.Errorf("PredictGW = %d, want 3", got.PredictGW)
	}
	if len(got.Bootstrap.Elements) != 2 {
		t.Fatalf("elements = %d, want 2", len(got.Bootstrap.Elements))
	}
	if len(got.Bootstrap.Teams) != 2 {
		t.Fatalf("teams = %d, want 2", len(got.Bootstrap.Teams))
	}
	if len(got.Fixtures) != 3 {
		t.Fatalf("fixtures = %d, want 3", len(got.Fixtures))
	}

	byID := map[int]int{}
	for i, p := range got.Bootstrap.Elements {
		byID[p.ID] = i
	}
	salah := got.Bootstrap.Elements[byID[1]]
	haaland := got.Bootstrap.Elements[byID[2]]

	// Season-to-date totals through GW2, no look-ahead into GW3.
	if salah.TotalPoints != 16 {
		t.Errorf("Salah TotalPoints = %d, want 16 (10+6)", salah.TotalPoints)
	}
	if haaland.TotalPoints != 17 {
		t.Errorf("Haaland TotalPoints = %d, want 17 (5+12)", haaland.TotalPoints)
	}

	// Form is the mean over the reconstructed window (GW1-2 here, since the
	// window is capped at the available history), not a running sum.
	if float64(salah.Form) != 8.0 {
		t.Errorf("Salah Form = %v, want 8.0 ((10+6)/2)", salah.Form)
	}
	if float64(haaland.Form) != 8.5 {
		t.Errorf("Haaland Form = %v, want 8.5 ((5+12)/2)", haaland.Form)
	}

	// Points per game over games actually played (minutes > 0).
	if float64(salah.PointsPerGame) != 8.0 {
		t.Errorf("Salah PointsPerGame = %v, want 8.0", salah.PointsPerGame)
	}

	// Latest value wins for now_cost.
	if salah.NowCost != 131 {
		t.Errorf("Salah NowCost = %d, want 131 (latest gw2 value)", salah.NowCost)
	}

	// Team join via teams.csv name <-> gw csv team field.
	if salah.Team != 1 {
		t.Errorf("Salah Team = %d, want 1 (Liverpool)", salah.Team)
	}
	if haaland.ElementType != 4 {
		t.Errorf("Haaland ElementType = %d, want 4 (FWD)", haaland.ElementType)
	}

	// Actual results come from predictGW itself (GW3), not summed state.
	if len(got.Actual) != 2 {
		t.Fatalf("actual = %d entries, want 2", len(got.Actual))
	}
	if got.Actual[1].Points != 2 {
		t.Errorf("Salah actual GW3 points = %d, want 2", got.Actual[1].Points)
	}
	if got.Actual[2].Minutes != 0 {
		t.Errorf("Haaland actual GW3 minutes = %d, want 0 (unused sub)", got.Actual[2].Minutes)
	}

	// Second BuildCase call must be served from the on-disk cache, not the
	// network — verified by pointing HTTP at a dead server and confirming
	// no error.
	c.HTTP = &http.Client{}
	c.BaseURL = "http://127.0.0.1:1" // nothing listening here
	if _, err := c.BuildCase(context.Background(), "2099-00", 3); err != nil {
		t.Errorf("second BuildCase (should hit cache): %v", err)
	}
}

func TestFuturePointsSumsRangeAndCountsAppearances(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	c := NewCorpus(t.TempDir())
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	got, err := c.FuturePoints(context.Background(), "2099-00", 2, 3)
	if err != nil {
		t.Fatalf("FuturePoints: %v", err)
	}

	salah := got[1]
	if salah.Points != 8 || salah.Minutes != 180 || salah.Appearances != 2 {
		t.Errorf("Salah = %+v, want {Points:8 Minutes:180 Appearances:2}", salah)
	}

	// Haaland's GW3 row has 0 minutes (an unused substitute) — should count
	// toward Points/Minutes but not Appearances.
	haaland := got[2]
	if haaland.Points != 12 || haaland.Minutes != 90 || haaland.Appearances != 1 {
		t.Errorf("Haaland = %+v, want {Points:12 Minutes:90 Appearances:1}", haaland)
	}
}

func TestBuildCaseRejectsGW1(t *testing.T) {
	c := NewCorpus(t.TempDir())
	if _, err := c.BuildCase(context.Background(), "2099-00", 1); err == nil {
		t.Error("BuildCase(gw=1) should error: no prior state to reconstruct")
	}
}

// TestBuildCaseRejectsPreSchemaSeasons guards against the real bug found
// when validating this package against vaastav's actual pre-2020-21 files:
// those gw CSVs have no "position"/"team" columns at all, which would
// otherwise silently zero out every player's ElementType and Team rather
// than failing loudly.
func TestBuildCaseRejectsPreSchemaSeasons(t *testing.T) {
	files := map[string]string{
		"/2016-17/teams.csv":    teamsCSV,
		"/2016-17/fixtures.csv": fixturesCSV,
		// No "position" or "team" columns — the real pre-2020-21 shape.
		"/2016-17/gws/gw1.csv": "element,name,total_points,minutes,value\n1,Salah,10,90,130\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := NewCorpus(t.TempDir())
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	_, err := c.BuildCase(context.Background(), "2016-17", 2)
	if err == nil {
		t.Fatal("BuildCase should reject a schema missing position/team columns, not silently zero them")
	}
	if !strings.Contains(err.Error(), "position") {
		t.Errorf("error should name the missing column, got: %v", err)
	}
}
