package remotecongestion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTeamFixtureCalendarNoURLIsOff(t *testing.T) {
	c := NewClient(Config{})
	if _, err := c.TeamFixtureCalendar(context.Background(), "2026-2027", 1, 5); err == nil {
		t.Fatal("expected an error with no URL configured")
	}
}

func TestTeamFixtureCalendarParsesAndCaches(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"3":["2025-09-16T18:45:00Z","2025-12-09T21:00:00Z"],"6":["2025-09-16T21:00:00Z"]}`))
	}))
	defer srv.Close()

	c := NewClient(Config{URL: srv.URL})
	c.HTTP = srv.Client()

	calendar, err := c.TeamFixtureCalendar(context.Background(), "2026-2027", 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(calendar[3]) != 2 || len(calendar[6]) != 1 {
		t.Fatalf("unexpected calendar: %+v", calendar)
	}
	want := time.Date(2025, 9, 16, 18, 45, 0, 0, time.UTC)
	if !calendar[3][0].Equal(want) {
		t.Errorf("calendar[3][0] = %v, want %v", calendar[3][0], want)
	}

	// A second call within the local TTL should use the in-memory cache.
	if _, err := c.TeamFixtureCalendar(context.Background(), "2026-2027", 1, 5); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("expected the second call to use the cache, got %d network hits", hits)
	}
}

func TestTeamFixtureCalendarSendsAuthHeaders(t *testing.T) {
	var gotAuth, gotID, gotSecret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotID = r.Header.Get("CF-Access-Client-Id")
		gotSecret = r.Header.Get("CF-Access-Client-Secret")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(Config{URL: srv.URL, AuthToken: "tok", AccessClientID: "cid", AccessClientKey: "csecret"})
	c.HTTP = srv.Client()
	if _, err := c.TeamFixtureCalendar(context.Background(), "2026-2027", 1, 5); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" || gotID != "cid" || gotSecret != "csecret" {
		t.Errorf("headers = (%q, %q, %q), want (Bearer tok, cid, csecret)", gotAuth, gotID, gotSecret)
	}
}

func TestTeamFixtureCalendarFailsSoftOnBadResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(Config{URL: srv.URL})
	c.HTTP = srv.Client()
	if _, err := c.TeamFixtureCalendar(context.Background(), "2026-2027", 1, 5); err == nil {
		t.Fatal("expected an error on a non-200 response")
	}
}

// Whitespace around env values (issue #28) must not break URL parsing or
// leak into the auth headers.
func TestConfigFromEnvTrimsWhitespaceAndFetches(t *testing.T) {
	var gotAuth, gotID, gotSecret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotID = r.Header.Get("CF-Access-Client-Id")
		gotSecret = r.Header.Get("CF-Access-Client-Secret")
		_, _ = w.Write([]byte(`{"3":["2025-09-16T18:45:00Z"]}`))
	}))
	defer srv.Close()

	t.Setenv("FPL_MCP_CONGESTION_URL", " "+srv.URL+" \n")
	t.Setenv("FPL_MCP_CONGESTION_TOKEN", " tok\t")
	t.Setenv("FPL_MCP_CONGESTION_ACCESS_CLIENT_ID", "\tcid ")
	t.Setenv("FPL_MCP_CONGESTION_ACCESS_CLIENT_SECRET", " csecret\n")

	cfg := ConfigFromEnv()
	if cfg.URL != srv.URL {
		t.Errorf("URL = %q, want %q", cfg.URL, srv.URL)
	}

	c := NewClient(cfg)
	c.HTTP = srv.Client()
	calendar, err := c.TeamFixtureCalendar(context.Background(), "2026-2027", 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(calendar[3]) != 1 {
		t.Errorf("unexpected calendar: %+v", calendar)
	}
	if gotAuth != "Bearer tok" || gotID != "cid" || gotSecret != "csecret" {
		t.Errorf("headers = (%q, %q, %q), want (Bearer tok, cid, csecret)", gotAuth, gotID, gotSecret)
	}
}
