package remoteweights

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/algo"
	"github.com/fantasypl/mcp/internal/store"
)

func TestLoadNoConfigIsOffAndNeverHitsNetwork(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer srv.Close()

	layout := store.Layout{Root: t.TempDir()}
	w, ok := Load(context.Background(), Config{}, layout, time.Now())
	if ok {
		t.Fatal("expected ok=false with no URL configured")
	}
	if w != algo.DefaultWeights() {
		t.Errorf("expected DefaultWeights(), got %+v", w)
	}
	if hits != 0 {
		t.Errorf("expected no network calls, got %d", hits)
	}
}

func TestLoadUsesFreshLocalCacheWithoutNetwork(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	layout := store.Layout{Root: t.TempDir()}
	now := time.Now()
	if err := layout.SaveOptimizedWeightsCache(&store.OptimizedWeightsCache{
		Weights:          map[string]float64{"xg90": 9.0},
		OptimizedAtEpoch: float64(now.UnixNano()) / 1e9,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := Config{URL: srv.URL}
	w, ok := Load(context.Background(), cfg, layout, now.Add(time.Minute))
	if !ok {
		t.Fatal("expected ok=true from a fresh local cache")
	}
	if w.XG90 != 9.0 {
		t.Errorf("XG90 = %v, want 9.0 (from local cache, not default)", w.XG90)
	}
	if hits != 0 {
		t.Errorf("expected the fresh local cache to skip the network, got %d hits", hits)
	}
}

func TestLoadFetchesAndCachesOnStaleOrMissingCache(t *testing.T) {
	body := `{"weights":{"xg90":7.5},"optimized_at_epoch":1700000000,"base_weights":{},"rolling_window":10}`
	var gotID, gotSecret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get("CF-Access-Client-Id")
		gotSecret = r.Header.Get("CF-Access-Client-Secret")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	layout := store.Layout{Root: t.TempDir()}
	cfg := Config{URL: srv.URL, AccessClientID: "cid", AccessClientKey: "secret"}
	now := time.Now()
	w, ok := Load(context.Background(), cfg, layout, now)
	if !ok {
		t.Fatal("expected ok=true on a successful fetch")
	}
	if w.XG90 != 7.5 {
		t.Errorf("XG90 = %v, want 7.5", w.XG90)
	}
	if gotID != "cid" || gotSecret != "secret" {
		t.Errorf("Access headers = (%q, %q), want (cid, secret)", gotID, gotSecret)
	}

	cache, ok, err := layout.LoadOptimizedWeightsCache()
	if err != nil || !ok {
		t.Fatalf("expected the fetched weights to be cached locally: ok=%v err=%v", ok, err)
	}
	if !cache.Fresh(localTTL, now) {
		t.Error("expected the just-written cache to be fresh as of the same instant")
	}
}

func TestLoadFallsBackToDefaultsOnFailure(t *testing.T) {
	tests := []struct {
		name string
		srv  *httptest.Server
	}{
		{"non-200 status", httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))},
		{"malformed JSON", httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer tt.srv.Close()
			layout := store.Layout{Root: t.TempDir()}
			cfg := Config{URL: tt.srv.URL}
			w, ok := Load(context.Background(), cfg, layout, time.Now())
			if ok {
				t.Error("expected ok=false")
			}
			if w != algo.DefaultWeights() {
				t.Errorf("expected DefaultWeights() on failure, got %+v", w)
			}
		})
	}
}

func TestLoadFallsBackOnUnreachableHost(t *testing.T) {
	layout := store.Layout{Root: t.TempDir()}
	cfg := Config{URL: "http://127.0.0.1:1/unreachable"}
	w, ok := Load(context.Background(), cfg, layout, time.Now())
	if ok {
		t.Error("expected ok=false for an unreachable host")
	}
	if w != algo.DefaultWeights() {
		t.Errorf("expected DefaultWeights(), got %+v", w)
	}
}

// A user_config value pasted with stray whitespace (issue #28) must still
// load remote weights rather than failing URL parsing and silently falling
// back to DefaultWeights().
func TestConfigFromEnvTrimsWhitespaceAndLoads(t *testing.T) {
	body := `{"weights":{"xg90":7.5},"optimized_at_epoch":1700000000,"base_weights":{},"rolling_window":10}`
	var gotID, gotSecret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get("CF-Access-Client-Id")
		gotSecret = r.Header.Get("CF-Access-Client-Secret")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	t.Setenv("FPL_MCP_WEIGHTS_URL", " "+srv.URL+" \n")
	t.Setenv("FPL_MCP_WEIGHTS_ACCESS_CLIENT_ID", "\tcid ")
	t.Setenv("FPL_MCP_WEIGHTS_ACCESS_CLIENT_SECRET", " secret\n")

	cfg := ConfigFromEnv()
	if cfg.URL != srv.URL {
		t.Errorf("URL = %q, want %q", cfg.URL, srv.URL)
	}

	layout := store.Layout{Root: t.TempDir()}
	w, ok := Load(context.Background(), cfg, layout, time.Now())
	if !ok {
		t.Fatal("expected ok=true: whitespace around the URL should not break loading")
	}
	if w.XG90 != 7.5 {
		t.Errorf("XG90 = %v, want 7.5 (from the remote fetch)", w.XG90)
	}
	if gotID != "cid" || gotSecret != "secret" {
		t.Errorf("headers = (%q, %q), want (cid, secret)", gotID, gotSecret)
	}
}
