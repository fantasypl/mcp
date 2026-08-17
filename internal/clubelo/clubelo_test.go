package clubelo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
)

// arsenalPageFixture is a trimmed but structurally real page: the header
// Elo line plus an embedded Vega-Lite spec with a Date/Elo dataset, in the
// same shape verified live against https://clubelo.com/Arsenal.
const arsenalPageFixture = `<html><body>
<div class="blatt"><h1><a href="/2026-08-14/Arsenal">Arsenal</a></h1>
<p>Elo: <b>2006</b> (Best: 2024, reached on 2026-03-07), Golo: 1.03</p>
</div>
<script type="text/javascript">
var vegaJson = {"$schema": "https://vega.github.io/schema/vega-lite/v5.20.1.json", "data": {"name": "data-abc123"}, "datasets": {"data-abc123": [
{"Date": "2025-08-16T00:00:00", "Elo": 1950.5, "Golo": 1.1, "segment_id": 0},
{"Date": "2025-09-01T00:00:00", "Elo": 1975.25, "Golo": 1.2, "segment_id": 0},
{"Date": "2026-05-30T00:00:00", "Elo": 2005.72, "Golo": 1.0, "segment_id": 0}
]}, "mark": "line"};
</script>
</body></html>`

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	files := map[string]string{
		"/Arsenal": arsenalPageFixture,
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

func newClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c := NewClient(filepath.Join(t.TempDir(), "cache"))
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()
	return c
}

func TestCurrentParsesHeaderElo(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	c := newClient(t, srv)

	got, err := c.Current(context.Background(), "Arsenal")
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got != 2006 {
		t.Errorf("Current = %v, want 2006", got)
	}
}

func TestHistoryDecodesEmbeddedChartData(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	c := newClient(t, srv)

	hist, err := c.History(context.Background(), "Arsenal")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("len = %d, want 3", len(hist))
	}
	if hist[0].Date != "2025-08-16" || hist[0].Elo != 1950.5 {
		t.Errorf("hist[0] = %+v, want {2025-08-16 1950.5}", hist[0])
	}
	if hist[2].Date != "2026-05-30" || hist[2].Elo != 2005.72 {
		t.Errorf("hist[2] = %+v, want {2026-05-30 2005.72}", hist[2])
	}
}

func TestByDatePicksLatestPointAtOrBefore(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	c := newClient(t, srv)

	r, ok, err := c.ByDate(context.Background(), "Arsenal", "2025-09-15")
	if err != nil {
		t.Fatalf("ByDate: %v", err)
	}
	if !ok {
		t.Fatal("ByDate should find a point")
	}
	if r.Date != "2025-09-01" || r.Elo != 1975.25 {
		t.Errorf("ByDate(2025-09-15) = %+v, want the 2025-09-01 point", r)
	}

	_, ok, err = c.ByDate(context.Background(), "Arsenal", "2020-01-01")
	if err != nil {
		t.Fatalf("ByDate (predates window): %v", err)
	}
	if ok {
		t.Error("ByDate should report not-found for a date before the history window, not extrapolate")
	}
}

func TestSlugForKnownAndUnknownClubs(t *testing.T) {
	if slug, ok := SlugFor("ARS"); !ok || slug != "Arsenal" {
		t.Errorf("SlugFor(ARS) = %q, %v; want Arsenal, true", slug, ok)
	}
	if slug, ok := SlugFor("TOT"); !ok || slug != "Tottenham" {
		t.Errorf("SlugFor(TOT) = %q, %v; want Tottenham, true", slug, ok)
	}
	if _, ok := SlugFor("XXX"); ok {
		t.Error("SlugFor(XXX) should report false for an unknown club, not guess")
	}
}

func TestCurrentByFPLTeamReportsUnmatched(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	c := newClient(t, srv)

	fplTeams := []fpl.Team{
		{ID: 1, Name: "Arsenal", ShortName: "ARS"},
		{ID: 99, Name: "Fictional United", ShortName: "FIC"},
	}

	got, err := c.CurrentByFPLTeam(context.Background(), fplTeams)
	if err == nil {
		t.Fatal("CurrentByFPLTeam should error when a team has no slug")
	}
	if !strings.Contains(err.Error(), "Fictional United") {
		t.Errorf("error should name the unmatched team, got: %v", err)
	}
	if got[1] != 2006 {
		t.Errorf("Arsenal should still be in the partial result, got %+v", got)
	}
}

func TestFetchRespectsCacheTTL(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte(arsenalPageFixture))
	}))
	defer srv.Close()
	c := newClient(t, srv)

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	if _, err := c.Current(context.Background(), "Arsenal"); err != nil {
		t.Fatalf("first Current: %v", err)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}

	now = now.Add(1 * time.Hour)
	if _, err := c.Current(context.Background(), "Arsenal"); err != nil {
		t.Fatalf("second Current: %v", err)
	}
	if hits != 1 {
		t.Fatalf("hits after cached call = %d, want 1", hits)
	}

	now = now.Add(DefaultTTL)
	if _, err := c.Current(context.Background(), "Arsenal"); err != nil {
		t.Fatalf("third Current: %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits after stale call = %d, want 2", hits)
	}
}
