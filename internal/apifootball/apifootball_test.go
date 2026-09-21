package apifootball

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/marketodds"
)

const fixturesJSON = `{"errors":[],"paging":{"current":1,"total":1},"response":[
 {"fixture":{"id":101},"teams":{"home":{"name":"Manchester United"},"away":{"name":"Tottenham"}}},
 {"fixture":{"id":102},"teams":{"home":{"name":"Arsenal"},"away":{"name":"Wolves"}}}]}`

func oddsPage(page, total int, fixtureID int, h, d, a string) string {
	return fmt.Sprintf(`{"errors":[],"paging":{"current":%d,"total":%d},"response":[
 {"fixture":{"id":%d},"bookmakers":[{"bets":[
  {"id":1,"values":[{"value":"Home","odd":"%s"},{"value":"Draw","odd":"%s"},{"value":"Away","odd":"%s"}]},
  {"id":5,"values":[{"value":"Over 2.5","odd":"1.80"},{"value":"Under 2.5","odd":"2.00"}]}]}]}]}`,
		page, total, fixtureID, h, d, a)
}

func fakeServer(t *testing.T, calls *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if r.Header.Get("x-apisports-key") != "k" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/fixtures":
			_, _ = w.Write([]byte(fixturesJSON))
		case "/odds":
			if r.URL.Query().Get("page") == "2" {
				_, _ = w.Write([]byte(oddsPage(2, 2, 102, "1.20", "7.0", "12.0")))
				return
			}
			_, _ = w.Write([]byte(oddsPage(1, 2, 101, "2.60", "3.50", "2.70")))
		default:
			http.NotFound(w, r)
		}
	}))
}

func newTestClient(srv *httptest.Server, dir string) *Client {
	return &Client{
		APIKey: "k", BaseURL: srv.URL, CacheDir: dir, TTL: time.Hour, HTTP: srv.Client(),
		now: func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) },
	}
}

func TestMatchModelsPagesAndCanonicalises(t *testing.T) {
	var calls int32
	srv := fakeServer(t, &calls)
	defer srv.Close()

	ms, err := newTestClient(srv, t.TempDir()).MatchModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("got %d matches, want 2 (one per odds page)", len(ms))
	}
	utdSpurs, ok := ms[marketodds.Key{Home: "Man Utd", Away: "Spurs"}]
	if !ok {
		t.Fatalf("names not canonicalised to FPL spelling; keys: %v", ms)
	}
	arsWolves := ms[marketodds.Key{Home: "Arsenal", Away: "Wolves"}]
	if arsWolves.HomeCS <= utdSpurs.HomeCS {
		t.Errorf("heavy favourite should have the better clean-sheet chance: %.2f vs %.2f", arsWolves.HomeCS, utdSpurs.HomeCS)
	}
	if calls != 3 { // fixtures + 2 odds pages
		t.Errorf("made %d requests, want 3", calls)
	}
}

func TestMatchModelsServesFreshCacheWithoutRequests(t *testing.T) {
	var calls int32
	srv := fakeServer(t, &calls)
	defer srv.Close()
	c := newTestClient(srv, t.TempDir())
	if _, err := c.MatchModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := atomic.LoadInt32(&calls)
	if _, err := c.MatchModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != before {
		t.Errorf("fresh cache still hit the network")
	}
}

func TestMatchModelsFallsBackToStaleCache(t *testing.T) {
	var calls int32
	srv := fakeServer(t, &calls)
	c := newTestClient(srv, t.TempDir())
	if _, err := c.MatchModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.Close() // upstream now unreachable
	c.now = func() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC) }
	ms, err := c.MatchModels(context.Background())
	if err != nil || len(ms) != 2 {
		t.Fatalf("expected stale cache fallback, got %d matches, err %v", len(ms), err)
	}
}

func TestAPIErrorEnvelopeIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":{"plan":"Free plans do not have access to this season."},"response":[]}`))
	}))
	defer srv.Close()
	if _, err := newTestClient(srv, t.TempDir()).MatchModels(context.Background()); err == nil {
		t.Fatal("expected the plan/season error to surface")
	}
}

func TestNoKey(t *testing.T) {
	var c *Client
	if _, err := c.MatchModels(context.Background()); err != ErrNoKey {
		t.Errorf("err = %v, want ErrNoKey", err)
	}
	t.Setenv(EnvKey, "")
	if FromEnv(t.TempDir()) != nil {
		t.Error("FromEnv should be nil without a key")
	}
}

func TestSeasonYear(t *testing.T) {
	for _, tc := range []struct {
		date time.Time
		want int
	}{
		{time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), 2026},
		{time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC), 2026},
		{time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC), 2025},
	} {
		if got := SeasonYear(tc.date); got != tc.want {
			t.Errorf("SeasonYear(%v) = %d, want %d", tc.date, got, tc.want)
		}
	}
}
