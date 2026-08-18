package insights

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

const pmsGW1CSV = `player_id,match_id,minutes_played
100,25-26-prem-team-a-vs-team-b,90
200,25-26-prem-team-a-vs-team-b,12
100,25-26-champions-league-team-a-vs-team-c,90
`

const pmsGW2CSV = `player_id,match_id,minutes_played
100,25-26-prem-team-b-vs-team-a,90
200,25-26-prem-team-b-vs-team-a,5
`

func newMinutesTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	files := map[string]string{
		"/2025-2026/By Gameweek/GW1/playermatchstats.csv": pmsGW1CSV,
		"/2025-2026/By Gameweek/GW2/playermatchstats.csv": pmsGW2CSV,
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

func TestPlayerMinutesInRangeAggregatesPremierLeagueOnly(t *testing.T) {
	srv := newMinutesTestServer(t)
	defer srv.Close()
	c := NewClient(filepath.Join(t.TempDir(), "cache"))
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	got, err := c.PlayerMinutesInRange(context.Background(), "2025-2026", 1, 2)
	if err != nil {
		t.Fatalf("PlayerMinutesInRange: %v", err)
	}

	starter := got[100]
	// Two Premier League appearances (90+90); the GW1 Champions League row
	// must not leak in.
	if starter.Matches != 2 || starter.SumMinutes != 180 {
		t.Fatalf("player 100 = %+v, want Matches=2 SumMinutes=180", starter)
	}
	if starter.AvgMinutes() != 90 {
		t.Errorf("AvgMinutes() = %v, want 90", starter.AvgMinutes())
	}

	sub := got[200]
	if sub.Matches != 2 || sub.SumMinutes != 17 {
		t.Fatalf("player 200 = %+v, want Matches=2 SumMinutes=17", sub)
	}
	if sub.AvgMinutes() >= 10 {
		t.Errorf("AvgMinutes() = %v, want well under 10 (mostly cameos)", sub.AvgMinutes())
	}
}

func TestPlayerMinutesInRangeNotAvailable(t *testing.T) {
	srv := newMinutesTestServer(t)
	defer srv.Close()
	c := NewClient(filepath.Join(t.TempDir(), "cache"))
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	_, err := c.PlayerMinutesInRange(context.Background(), "2025-2026", 10, 15)
	if !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("err = %v, want ErrNotAvailable", err)
	}
}
