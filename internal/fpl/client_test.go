package fpl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testClient wires a Client to a test server with a controllable clock and a
// sleep that records rather than waits, so retry timing is asserted without
// real delays.
func testClient(t *testing.T, h http.HandlerFunc) (*Client, *[]time.Duration, func(time.Duration)) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	var mu sync.Mutex
	clock := time.Now()
	var slept []time.Duration

	c := NewClient()
	c.BaseURL = srv.URL
	c.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	c.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
		return nil
	}
	advance := func(d time.Duration) {
		mu.Lock()
		clock = clock.Add(d)
		mu.Unlock()
	}
	return c, &slept, advance
}

func TestCacheHit(t *testing.T) {
	var hits int32
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(`{"elements":[],"teams":[],"events":[],"element_types":[]}`))
	})

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := c.Bootstrap(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if hits != 1 {
		t.Errorf("upstream requests = %d, want 1", hits)
	}
}

func TestCacheExpiry(t *testing.T) {
	var hits int32
	c, _, advance := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(`{"elements":[]}`))
	})

	ctx := context.Background()
	c.Bootstrap(ctx)
	advance(DefaultTTL - time.Second)
	c.Bootstrap(ctx)
	if hits != 1 {
		t.Fatalf("refetched before TTL expiry: hits = %d", hits)
	}
	advance(2 * time.Second)
	c.Bootstrap(ctx)
	if hits != 2 {
		t.Errorf("did not refetch after TTL expiry: hits = %d", hits)
	}
}

// Live data uses a much shorter TTL than bootstrap; mixing them up would serve
// five-minute-old scores during a match.
func TestPerEndpointTTL(t *testing.T) {
	var hits int32
	c, _, advance := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(`{}`))
	})

	ctx := context.Background()
	c.LivePoints(ctx, 1)
	advance(LiveTTL + time.Second)
	c.LivePoints(ctx, 1)
	if hits != 2 {
		t.Errorf("live TTL not honoured: hits = %d, want 2", hits)
	}

	atomic.StoreInt32(&hits, 0)
	c.EventStatus(ctx)
	advance(EntryTTL - time.Second)
	c.EventStatus(ctx)
	if hits != 1 {
		t.Errorf("entry TTL not honoured: hits = %d, want 1", hits)
	}
}

// The plan's explicit verification item: concurrent cold-cache calls must
// produce exactly one upstream fetch, not one per caller.
func TestSingleflightCollapsesConcurrentMisses(t *testing.T) {
	var hits int32
	release := make(chan struct{})
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		<-release // hold the request open so all callers pile up behind it
		w.Write([]byte(`{"elements":[],"teams":[],"events":[],"element_types":[]}`))
	})

	const callers = 25
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Bootstrap(context.Background()); err != nil {
				errs <- err
			}
		}()
	}

	// Give every goroutine a chance to reach the singleflight barrier.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("caller failed: %v", err)
	}
	if hits != 1 {
		t.Errorf("upstream requests = %d, want 1 (singleflight did not collapse)", hits)
	}
}

func TestRetryThenSucceed(t *testing.T) {
	var hits int32
	c, slept, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"elements":[]}`))
	})

	if _, err := c.Bootstrap(context.Background()); err != nil {
		t.Fatalf("expected success on third attempt: %v", err)
	}
	if hits != 3 {
		t.Errorf("attempts = %d, want 3", hits)
	}
	// Ported policy: linear backoff of 1s then 2s.
	want := []time.Duration{time.Second, 2 * time.Second}
	if len(*slept) != len(want) {
		t.Fatalf("backoff = %v, want %v", *slept, want)
	}
	for i := range want {
		if (*slept)[i] != want[i] {
			t.Errorf("backoff[%d] = %v, want %v", i, (*slept)[i], want[i])
		}
	}
}

func TestRetryExhaustion(t *testing.T) {
	var hits int32
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	_, err := c.Bootstrap(context.Background())
	if err == nil {
		t.Fatal("expected failure")
	}
	if hits != maxRetries+1 {
		t.Errorf("attempts = %d, want %d", hits, maxRetries+1)
	}
}

// Malformed JSON is not transient; retrying it just triples the latency of a
// guaranteed failure.
func TestNoRetryOnMalformedJSON(t *testing.T) {
	var hits int32
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(`{not json`))
	})

	if _, err := c.Bootstrap(context.Background()); err == nil {
		t.Fatal("expected decode failure")
	}
	if hits != 1 {
		t.Errorf("attempts = %d, want 1 (decode errors must not retry)", hits)
	}
}

// The FPL API rejects requests without a browser-shaped User-Agent, so this is
// load-bearing rather than cosmetic.
func TestUserAgentSent(t *testing.T) {
	got := make(chan string, 1)
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("User-Agent")
		w.Write([]byte(`{}`))
	})
	c.EventStatus(context.Background())
	if ua := <-got; ua != userAgent {
		t.Errorf("User-Agent = %q, want the browser string", ua)
	}
}

func TestContextCancellation(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := c.Bootstrap(ctx); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestClearCache(t *testing.T) {
	var hits int32
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(`{"elements":[]}`))
	})
	ctx := context.Background()
	c.Bootstrap(ctx)
	c.ClearCache()
	c.Bootstrap(ctx)
	if hits != 2 {
		t.Errorf("hits = %d, want 2 after ClearCache", hits)
	}
}

// End to end against the real frozen payload: the client must decode 1.3 MB of
// production JSON into the typed model without loss.
func TestClientDecodesRealPayload(t *testing.T) {
	body, err := os.ReadFile("../../testdata/bootstrap_preseason.json")
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})

	bs, err := c.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bs.Elements) != 564 || len(bs.Teams) != 20 || len(bs.Events) != 38 {
		t.Errorf("decoded %d elements, %d teams, %d events",
			len(bs.Elements), len(bs.Teams), len(bs.Events))
	}
	if bs.NextGameweek() != 1 {
		t.Errorf("NextGameweek = %d, want 1", bs.NextGameweek())
	}
}

func TestFixturesEndpoint(t *testing.T) {
	body, err := os.ReadFile("../../testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})
	fs, err := c.Fixtures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 380 {
		t.Errorf("fixtures = %d, want 380", len(fs))
	}
}

func TestEventStatusDecoding(t *testing.T) {
	const payload = `{"status":[{"bonus_added":true,"date":"2026-03-18"}],"leagues":"Updated"}`
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	})
	got, err := c.EventStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Status) != 1 || !got.Status[0].BonusAdded || got.Status[0].Date != "2026-03-18" {
		t.Errorf("got %+v", got)
	}
	if got.Leagues != "Updated" {
		t.Errorf("Leagues = %q, want %q", got.Leagues, "Updated")
	}
	if !got.BonusConfirmed() {
		t.Error("BonusConfirmed() should be true when every day has bonus_added=true")
	}
}

func TestLivePointsDecoding(t *testing.T) {
	const payload = `{"elements":[{"id":411,"stats":{"minutes":90,"total_points":12,"bps":34,"bonus":3}}]}`
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	})
	got, err := c.LivePoints(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Elements) != 1 {
		t.Fatalf("got %d elements, want 1", len(got.Elements))
	}
	e := got.Elements[0]
	if e.ID != 411 || e.Stats.Minutes != 90 || e.Stats.TotalPoints != 12 || e.Stats.BPS != 34 || e.Stats.Bonus != 3 {
		t.Errorf("got %+v", e)
	}
	if pts := got.ActualPoints(); pts[411] != 12 {
		t.Errorf("ActualPoints()[411] = %d, want 12", pts[411])
	}
}
