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

func newTestServer(t *testing.T, hits *int) *httptest.Server {
	t.Helper()
	// Go's net/http decodes %20 back to a literal space in r.URL.Path, so
	// keys here use the decoded form even though the client requests the
	// %20-escaped one.
	files := map[string]string{
		"/2025-2026/teams.csv":                            teamsCSV,
		"/2025-2026/By Gameweek/GW1/playermatchstats.csv": playermatchstatsCSV,
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
