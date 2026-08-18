package insights

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

const positionsGW1CSV = `match_id,team_side,player_id,player_name,jersey_number,position,x,y
25-26-prem-team-a-vs-team-b,home,100,Advancer,7,M,40.0,50.0
25-26-prem-team-a-vs-team-b,home,200,Steady,5,D,30.0,40.0
25-26-champions-league-team-a-vs-team-c,home,100,Advancer,7,M,90.0,50.0
`

const positionsGW2CSV = `match_id,team_side,player_id,player_name,jersey_number,position,x,y
25-26-prem-team-b-vs-team-a,away,100,Advancer,7,M,44.0,50.0
25-26-prem-team-b-vs-team-a,away,200,Steady,5,D,32.0,40.0
`

const positionsGW5CSV = `match_id,team_side,player_id,player_name,jersey_number,position,x,y
25-26-prem-team-a-vs-team-d,home,100,Advancer,7,M,70.0,55.0
25-26-prem-team-a-vs-team-d,home,200,Steady,5,D,29.0,41.0
`

func newPositionsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	files := map[string]string{
		"/2025-2026/By Gameweek/GW1/average_positions.csv": positionsGW1CSV,
		"/2025-2026/By Gameweek/GW2/average_positions.csv": positionsGW2CSV,
		"/2025-2026/By Gameweek/GW5/average_positions.csv": positionsGW5CSV,
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

func TestAveragePositionsAggregatesPremierLeagueOnly(t *testing.T) {
	srv := newPositionsTestServer(t)
	defer srv.Close()
	c := NewClient(filepath.Join(t.TempDir(), "cache"))
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	got, err := c.AveragePositions(context.Background(), "2025-2026", 1, 2)
	if err != nil {
		t.Fatalf("AveragePositions: %v", err)
	}

	adv := got[100]
	// Two Premier League appearances (GW1 home, GW2 away); the GW1
	// Champions League row must not leak in.
	if adv.Matches != 2 {
		t.Fatalf("Advancer.Matches = %d, want 2", adv.Matches)
	}
	wantAvgX := (40.0 + 44.0) / 2
	if diff := adv.AvgX() - wantAvgX; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("Advancer.AvgX() = %v, want %v", adv.AvgX(), wantAvgX)
	}

	steady := got[200]
	if steady.Matches != 2 {
		t.Fatalf("Steady.Matches = %d, want 2", steady.Matches)
	}
}

func TestAveragePositionsNotAvailable(t *testing.T) {
	srv := newPositionsTestServer(t)
	defer srv.Close()
	c := NewClient(filepath.Join(t.TempDir(), "cache"))
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	_, err := c.AveragePositions(context.Background(), "2025-2026", 10, 15)
	if !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("err = %v, want ErrNotAvailable", err)
	}
}

func TestComputePositionDrift(t *testing.T) {
	srv := newPositionsTestServer(t)
	defer srv.Close()
	c := NewClient(filepath.Join(t.TempDir(), "cache"))
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	baseline, err := c.AveragePositions(context.Background(), "2025-2026", 1, 2)
	if err != nil {
		t.Fatalf("baseline AveragePositions: %v", err)
	}
	recent, err := c.AveragePositions(context.Background(), "2025-2026", 5, 5)
	if err != nil {
		t.Fatalf("recent AveragePositions: %v", err)
	}

	drift := ComputePositionDrift(baseline, recent)

	adv := drift[100]
	if adv.BaselineMatches != 2 || adv.RecentMatches != 1 {
		t.Fatalf("Advancer matches = baseline %d recent %d, want 2 and 1", adv.BaselineMatches, adv.RecentMatches)
	}
	if adv.DeltaX() <= 0 {
		t.Errorf("Advancer DeltaX = %v, want positive (70.0 recent vs 42.0 baseline)", adv.DeltaX())
	}
	// This fixture only gives Advancer 2 baseline and 1 recent match — below
	// minMatchesForPositionDrift (3) — so the drift is real but not yet
	// trustworthy, and Qualified must say so.
	if adv.Qualified() {
		t.Error("Advancer has too few matches per window to qualify, but Qualified() returned true")
	}

	steady := drift[200]
	if steady.DeltaX() >= 2.0 {
		t.Errorf("Steady DeltaX = %v, want roughly flat", steady.DeltaX())
	}
}
