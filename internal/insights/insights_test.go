package insights

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

const teamsCSV = `code,id,name,short_name,elo,fotmob_name
3,1,Arsenal,ARS,2064,Arsenal
7,2,Aston Villa,AVL,1921,Aston Villa
`

const playermatchstatsCSV = `player_id,match_id,minutes_played,start_min,finish_min,goals
443,25-26-prem-manchester-united-vs-arsenal,79,0,79,0
266,25-26-prem-manchester-united-vs-arsenal,90,0,90,1
`

const clFixturesCSV = `gameweek,kickoff_time,home_team,away_team,match_id,tournament
4,2025-09-16T16:45:00+00:00,3,,25-26-champions-league-arsenal-vs-athletic-club,champions-league
`

func newTestServer(t *testing.T, hits *int) *httptest.Server {
	t.Helper()
	// Go's net/http decodes %20 back to a literal space in r.URL.Path, so
	// keys here use the decoded form even though the client requests the
	// %20-escaped one.
	files := map[string]string{
		"/2025-2026/teams.csv":                                       teamsCSV,
		"/2025-2026/By Gameweek/GW1/playermatchstats.csv":            playermatchstatsCSV,
		"/2025-2026/By Tournament/Champions League/GW4/fixtures.csv": clFixturesCSV,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
}

func newClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c := NewClient(filepath.Join(t.TempDir(), "cache"))
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()
	c.RetryBackoff = time.Millisecond // keep retry tests fast
	return c
}

func TestTeamElosJoinsDirectlyOnFPLID(t *testing.T) {
	srv := newTestServer(t, nil)
	defer srv.Close()
	c := newClient(t, srv)

	got, err := c.TeamElos(context.Background(), "2025-2026")
	if err != nil {
		t.Fatalf("TeamElos: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[1].Elo != 2064 {
		t.Errorf("Arsenal (id=1) Elo = %v, want 2064", got[1].Elo)
	}
	if got[2].Elo != 1921 {
		t.Errorf("Aston Villa (id=2) Elo = %v, want 1921", got[2].Elo)
	}
}

func TestGameweekFileSpaceInPath(t *testing.T) {
	srv := newTestServer(t, nil)
	defer srv.Close()
	c := newClient(t, srv)

	rows, err := c.GameweekFile(context.Background(), "2025-2026", 1, "playermatchstats.csv")
	if err != nil {
		t.Fatalf("GameweekFile: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}

	byID := RowsByID(rows, "player_id")
	row, ok := LookupID(byID, 443)
	if !ok {
		t.Fatal("player 443 should be found")
	}
	if Int(row["start_min"]) != 0 || Int(row["finish_min"]) != 79 {
		t.Errorf("player 443 start/finish = %d/%d, want 0/79", Int(row["start_min"]), Int(row["finish_min"]))
	}

	if _, ok := LookupID(byID, 99999); ok {
		t.Error("player 99999 should not be found — fallback should report a clean miss")
	}
}

func TestTournamentGameweekFile(t *testing.T) {
	srv := newTestServer(t, nil)
	defer srv.Close()
	c := newClient(t, srv)

	rows, err := c.TournamentGameweekFile(context.Background(), "2025-2026", "Champions League", 4, "fixtures.csv")
	if err != nil {
		t.Fatalf("TournamentGameweekFile: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0]["home_team"] != "3" {
		t.Errorf("home_team = %q, want %q (Arsenal's FPL code)", rows[0]["home_team"], "3")
	}

	// A competition round that doesn't fall in this gameweek is the normal
	// case, not an error.
	_, err = c.TournamentGameweekFile(context.Background(), "2025-2026", "Champions League", 5, "fixtures.csv")
	if !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("err = %v, want ErrNotAvailable", err)
	}
}

func TestGameweekFileNotAvailable(t *testing.T) {
	srv := newTestServer(t, nil)
	defer srv.Close()
	c := newClient(t, srv)

	// GW2's playermatchstats.csv doesn't exist on the test server — mirrors
	// a gameweek not yet played, or the enrichment tier lagging behind the
	// base files on a new season.
	_, err := c.GameweekFile(context.Background(), "2025-2026", 2, "playermatchstats.csv")
	if !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("err = %v, want ErrNotAvailable", err)
	}
}

// TestFetchRetriesTransientFailures guards against the real bug found
// verifying this package against the live GitHub CDN: a transport-level
// error (a request that fails before any HTTP status is even returned —
// timeout, reset, etc.) bypassed retry entirely, only status-code failures
// like 503 were retried. A single flaky request used to fail the whole
// fetch outright.
func TestFetchRetriesTransientFailures(t *testing.T) {
	var calls int
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("simulated transport failure")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
			Header:     make(http.Header),
		}, nil
	})

	c := NewClient(t.TempDir())
	c.HTTP = &http.Client{Transport: rt}
	c.RetryBackoff = time.Millisecond

	_, err := c.GameweekFile(context.Background(), "2025-2026", 1, "lineups.csv")
	if err != nil {
		t.Fatalf("GameweekFile should succeed after retrying transport failures: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (2 failures + 1 success)", calls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestFetchRespectsCacheTTL(t *testing.T) {
	var hits int
	srv := newTestServer(t, &hits)
	defer srv.Close()
	c := newClient(t, srv)

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	if _, err := c.TeamElos(context.Background(), "2025-2026"); err != nil {
		t.Fatalf("first TeamElos: %v", err)
	}
	if hits != 1 {
		t.Fatalf("hits after first call = %d, want 1", hits)
	}

	// Within TTL: served from cache, no new request.
	now = now.Add(1 * time.Hour)
	if _, err := c.TeamElos(context.Background(), "2025-2026"); err != nil {
		t.Fatalf("second TeamElos: %v", err)
	}
	if hits != 1 {
		t.Fatalf("hits after cached call = %d, want 1 (should not refetch within TTL)", hits)
	}

	// Past TTL: refetches.
	now = now.Add(DefaultTTL)
	if _, err := c.TeamElos(context.Background(), "2025-2026"); err != nil {
		t.Fatalf("third TeamElos: %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits after stale call = %d, want 2 (should refetch past TTL)", hits)
	}
}
