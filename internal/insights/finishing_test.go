package insights

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

const shotsGW1CSV = `player_id,player_name,match_id,outcome,xgot
100,Underperformer,25-26-prem-team-a-vs-team-b,save,0.60
100,Underperformer,25-26-prem-team-a-vs-team-b,save,0.50
100,Underperformer,25-26-prem-team-a-vs-team-b,miss,
200,Overperformer,25-26-prem-team-a-vs-team-b,goal,0.30
200,Overperformer,25-26-prem-team-a-vs-team-b,goal,0.20
300,EuroOnly,25-26-champions-league-team-a-vs-team-c,goal,0.90
`

const shotsGW2CSV = `player_id,player_name,match_id,outcome,xgot
100,Underperformer,25-26-prem-team-b-vs-team-a,save,0.40
100,Underperformer,25-26-prem-team-b-vs-team-a,save,0.30
100,Underperformer,25-26-prem-team-b-vs-team-a,save,0.20
200,Overperformer,25-26-prem-team-b-vs-team-a,goal,0.10
`

func newFinishingTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	files := map[string]string{
		"/2025-2026/By Gameweek/GW1/shots.csv": shotsGW1CSV,
		"/2025-2026/By Gameweek/GW2/shots.csv": shotsGW2CSV,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
}

func TestFinishingLuckAggregatesOnTargetPremierLeagueShotsOnly(t *testing.T) {
	srv := newFinishingTestServer(t)
	defer srv.Close()
	c := NewClient(filepath.Join(t.TempDir(), "cache"))
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	got, err := c.FinishingLuck(context.Background(), "2025-2026", 1, 2)
	if err != nil {
		t.Fatalf("FinishingLuck: %v", err)
	}

	under := got[100]
	// 5 on-target shots (2 saves in GW1 + 3 saves in GW2), 0 goals, since the
	// GW1 "miss" has a blank xgot and is excluded.
	if under.ShotsOnTarget != 5 || under.ActualGoals != 0 {
		t.Errorf("Underperformer = %+v, want ShotsOnTarget=5 ActualGoals=0", under)
	}
	wantXGOT := 0.60 + 0.50 + 0.40 + 0.30 + 0.20
	if diff := under.SumXGOT - wantXGOT; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("Underperformer SumXGOT = %v, want %v", under.SumXGOT, wantXGOT)
	}
	if under.Delta() >= 0 {
		t.Errorf("Underperformer Delta = %v, want negative (underperforming shot quality)", under.Delta())
	}
	if !under.Qualified() {
		t.Errorf("Underperformer should qualify with %d shots on target (min %d)", under.ShotsOnTarget, minShotsOnTarget)
	}

	over := got[200]
	if over.ShotsOnTarget != 3 || over.ActualGoals != 3 {
		t.Errorf("Overperformer = %+v, want ShotsOnTarget=3 ActualGoals=3", over)
	}
	if over.Delta() <= 0 {
		t.Errorf("Overperformer Delta = %v, want positive (outperforming shot quality)", over.Delta())
	}

	// Champions League shot must not leak into the Premier League signal.
	if _, ok := got[300]; ok {
		t.Error("player 300's shot was a Champions League match and should be excluded")
	}
}

func TestFinishingLuckNotAvailable(t *testing.T) {
	srv := newFinishingTestServer(t)
	defer srv.Close()
	c := NewClient(filepath.Join(t.TempDir(), "cache"))
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	// GW10-15 don't exist on the test server at all — mirrors a season
	// shots.csv doesn't cover.
	_, err := c.FinishingLuck(context.Background(), "2025-2026", 10, 15)
	if !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("err = %v, want ErrNotAvailable", err)
	}
}
