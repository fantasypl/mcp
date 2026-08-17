package insights

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

const congestionGW4CSV = `home_team,away_team,kickoff_time
1,2,2026-01-06T20:00:00Z
`

const congestionGW5CSV = `home_team,away_team,kickoff_time
3,1,2026-01-10T15:00:00Z
`

func newCongestionTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	files := map[string]string{
		"/2025-2026/By Gameweek/GW4/fixtures.csv": congestionGW4CSV,
		"/2025-2026/By Gameweek/GW5/fixtures.csv": congestionGW5CSV,
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

func TestTeamFixtureCalendarMergesAcrossGameweeks(t *testing.T) {
	srv := newCongestionTestServer(t)
	defer srv.Close()
	c := NewClient(filepath.Join(t.TempDir(), "cache"))
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	got, err := c.TeamFixtureCalendar(context.Background(), "2025-2026", 4, 5)
	if err != nil {
		t.Fatalf("TeamFixtureCalendar: %v", err)
	}

	// Team code 1 plays in both GW4 (as home) and GW5 (as away) — both
	// should land in its sorted calendar.
	if len(got[1]) != 2 {
		t.Fatalf("team 1 calendar = %v, want 2 fixtures", got[1])
	}
	if !got[1][0].Before(got[1][1]) {
		t.Errorf("team 1 calendar not sorted ascending: %v", got[1])
	}

	if len(got[2]) != 1 || len(got[3]) != 1 {
		t.Errorf("team 2/3 should have exactly 1 fixture each, got %d/%d", len(got[2]), len(got[3]))
	}
}

func TestTeamFixtureCalendarNotAvailable(t *testing.T) {
	srv := newCongestionTestServer(t)
	defer srv.Close()
	c := NewClient(filepath.Join(t.TempDir(), "cache"))
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	_, err := c.TeamFixtureCalendar(context.Background(), "2025-2026", 10, 15)
	if !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("err = %v, want ErrNotAvailable", err)
	}
}

func TestRestDaysBefore(t *testing.T) {
	dates := []time.Time{
		time.Date(2026, 1, 6, 20, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 20, 15, 0, 0, 0, time.UTC),
	}

	// 4 days after the first fixture, well before the second.
	ref := time.Date(2026, 1, 10, 15, 0, 0, 0, time.UTC)
	rest, ok := RestDaysBefore(dates, ref)
	if !ok {
		t.Fatal("want a prior fixture found")
	}
	if rest < 3.7 || rest > 3.8 {
		t.Errorf("rest = %v, want ~3.79 days", rest)
	}

	// Before any known fixture: no prior date.
	early := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	if _, ok := RestDaysBefore(dates, early); ok {
		t.Error("want no prior fixture before the calendar starts")
	}
}
