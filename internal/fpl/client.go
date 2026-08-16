package fpl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// The TTLs are behavioural, not incidental — live scores go stale in seconds
// while bootstrap-static is ~1.3 MB and changes slowly, so each endpoint's
// TTL is tuned to its own staleness rather than sharing one blanket value.
const (
	DefaultBaseURL = "https://fantasy.premierleague.com/api"

	DefaultTTL = 300 * time.Second // bootstrap, fixtures, element-summary
	LiveTTL    = 30 * time.Second  // event/{gw}/live
	EntryTTL   = 60 * time.Second  // entry picks, history, event-status
	NoCache    = time.Duration(-1) // explicit bypass; distinct from a zero value

	maxRetries     = 2
	retryDelay     = time.Second
	requestTimeout = 10 * time.Second

	// The FPL API rejects requests without a browser-shaped User-Agent.
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

type entry struct {
	val     any
	expires time.Time
}

// Client fetches from the FPL API with a TTL cache.
//
// Two concurrency properties are deliberate:
//
//   - The cache is mutex-guarded, so concurrent requests cannot race while
//     reading or updating it.
//   - Concurrent misses for the same URL collapse via singleflight, so a cold
//     start with several algorithms in flight triggers one bootstrap fetch
//     rather than one per caller.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	mu    sync.RWMutex
	cache map[string]entry
	sf    singleflight.Group

	// Injected for deterministic tests.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

// NewClient returns a Client with the package defaults.
func NewClient() *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: requestTimeout},
		cache:   make(map[string]entry),
		now:     time.Now,
		sleep:   sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ClearCache drops every cached entry.
func (c *Client) ClearCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]entry)
}

func (c *Client) lookup(url string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.cache[url]
	if !ok || c.now().After(e.expires) {
		return nil, false
	}
	return e.val, true
}

func (c *Client) store(url string, v any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[url] = entry{val: v, expires: c.now().Add(ttl)}
}

// HTTPError is a non-2xx response. It is retried like a transport error.
type HTTPError struct {
	StatusCode int
	URL        string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("fpl: %s returned %d", e.URL, e.StatusCode)
}

// fetch retrieves and decodes path into T, honouring the cache.
//
// It is a package-level function rather than a method because Go methods
// cannot carry their own type parameters.
func fetch[T any](ctx context.Context, c *Client, path string, ttl time.Duration) (T, error) {
	var zero T
	url := c.BaseURL + path

	if ttl > 0 {
		if v, ok := c.lookup(url); ok {
			if typed, ok := v.(T); ok {
				return typed, nil
			}
			// A type mismatch means two endpoints share a URL with different
			// shapes, which would be a bug here rather than bad input.
			return zero, fmt.Errorf("fpl: cached value for %s has unexpected type %T", url, v)
		}
	}

	// Collapse concurrent misses. The shared result is the decoded value, so
	// waiters neither refetch nor re-decode.
	v, err, _ := c.sf.Do(url, func() (any, error) {
		// Another goroutine may have populated the cache while we queued.
		if ttl > 0 {
			if v, ok := c.lookup(url); ok {
				return v, nil
			}
		}
		out, err := do[T](ctx, c, url)
		if err != nil {
			return nil, err
		}
		if ttl > 0 {
			c.store(url, out, ttl)
		}
		return out, nil
	})
	if err != nil {
		return zero, err
	}
	typed, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("fpl: %s decoded to unexpected type %T", url, v)
	}
	return typed, nil
}

// do performs the request with the retry policy: up to 3 attempts with
// a linear 1s, 2s backoff.
func do[T any](ctx context.Context, c *Client, url string) (T, error) {
	var zero T
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		out, err := attemptOnce[T](ctx, c, url)
		if err == nil {
			return out, nil
		}
		lastErr = err

		// A malformed body will not fix itself, and a cancelled context must
		// not be retried.
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		var de *json.SyntaxError
		if ok := asJSONError(err, &de); ok {
			return zero, err
		}

		if attempt < maxRetries {
			if err := c.sleep(ctx, retryDelay*time.Duration(attempt+1)); err != nil {
				return zero, err
			}
		}
	}
	return zero, fmt.Errorf("fpl: %s failed after %d attempts: %w", url, maxRetries+1, lastErr)
}

func attemptOnce[T any](ctx context.Context, c *Client, url string) (T, error) {
	var zero T

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return zero, &HTTPError{StatusCode: resp.StatusCode, URL: url}
	}

	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return zero, fmt.Errorf("fpl: decode %s: %w", url, err)
	}
	return out, nil
}

func asJSONError(err error, target **json.SyntaxError) bool {
	for err != nil {
		if e, ok := err.(*json.SyntaxError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ---------------------------------------------------------------------------
// Endpoint wrappers, one per FPL API path.
// ---------------------------------------------------------------------------

// Bootstrap returns GET /bootstrap-static/ — every player, team, gameweek and
// scoring rule. ~1.3 MB, so it is cached aggressively.
func (c *Client) Bootstrap(ctx context.Context) (*Bootstrap, error) {
	return fetch[*Bootstrap](ctx, c, "/bootstrap-static/", DefaultTTL)
}

// Fixtures returns GET /fixtures/ — all 380 fixtures with FDR ratings.
func (c *Client) Fixtures(ctx context.Context) ([]Fixture, error) {
	return fetch[[]Fixture](ctx, c, "/fixtures/", DefaultTTL)
}

// LivePoints returns GET /event/{gw}/live/ — real-time points during matches.
func (c *Client) LivePoints(ctx context.Context, gw int) (*LiveResponse, error) {
	return fetch[*LiveResponse](ctx, c, fmt.Sprintf("/event/%d/live/", gw), LiveTTL)
}

// PlayerSummary returns GET /element-summary/{id}/ — per-player history and
// upcoming fixtures.
func (c *Client) PlayerSummary(ctx context.Context, playerID int) (*PlayerSummary, error) {
	return fetch[*PlayerSummary](ctx, c, fmt.Sprintf("/element-summary/%d/", playerID), DefaultTTL)
}

// TeamPicks returns GET /entry/{team}/event/{gw}/picks/ — a manager's squad
// for that gameweek. See TeamPicks' doc comment for why this, unlike every
// other endpoint here, cannot be sourced from anywhere but a live season.
func (c *Client) TeamPicks(ctx context.Context, teamID, gw int) (*TeamPicks, error) {
	return fetch[*TeamPicks](ctx, c, fmt.Sprintf("/entry/%d/event/%d/picks/", teamID, gw), EntryTTL)
}

// TeamHistory returns GET /entry/{team}/history/.
func (c *Client) TeamHistory(ctx context.Context, teamID int) (*TeamHistory, error) {
	return fetch[*TeamHistory](ctx, c, fmt.Sprintf("/entry/%d/history/", teamID), DefaultTTL)
}

// LeagueTTL is shorter than most endpoint TTLs since a live league table
// shifts as gameweeks progress, but long enough that repeated calls within a
// session do not hammer the API.
const LeagueTTL = 120 * time.Second

// LeagueStandings returns GET /leagues-classic/{league_id}/standings/.
func (c *Client) LeagueStandings(ctx context.Context, leagueID int) (*LeagueStandings, error) {
	return fetch[*LeagueStandings](ctx, c, fmt.Sprintf("/leagues-classic/%d/standings/", leagueID), LeagueTTL)
}

// ManagerTransfers returns GET /entry/{team_id}/transfers/ — every transfer a
// manager has made this season, most recent first.
func (c *Client) ManagerTransfers(ctx context.Context, teamID int) ([]ManagerTransfer, error) {
	return fetch[[]ManagerTransfer](ctx, c, fmt.Sprintf("/entry/%d/transfers/", teamID), LeagueTTL)
}

// EventStatus returns GET /event-status/ — whether bonus points are confirmed.
func (c *Client) EventStatus(ctx context.Context) (*EventStatusResponse, error) {
	return fetch[*EventStatusResponse](ctx, c, "/event-status/", EntryTTL)
}
