// Package remotecongestion reads a centrally captured, already-resolved
// Champions League fixture calendar from a private, self-hosted mirror
// (fantasypl/data's congestion/champions-league.json, built by fplctl
// congestion-capture from internal/openfootball) — for the maintainer's own
// fpl-mcp instance only. It is entirely inert unless FPL_MCP_CONGESTION_URL
// is set: every public `go install` user keeps using FPL-Core-Insights'
// broader (but not self-hosted) congestion signal, exactly as before this
// package existed.
package remotecongestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// Config points Client at the endpoint serving the captured calendar.
// AccessClientID/AccessClientKey follow the same Cloudflare Access
// convention as internal/remoteweights; AuthToken instead sends a plain
// Authorization: Bearer header, for fetching straight from a private
// GitHub repo the way internal/vaastav's Corpus.AuthToken does — whichever
// fits how the endpoint is actually exposed.
type Config struct {
	URL             string
	AuthToken       string
	AccessClientID  string
	AccessClientKey string
}

// ConfigFromEnv reads FPL_MCP_CONGESTION_URL, FPL_MCP_CONGESTION_TOKEN, and
// the two FPL_MCP_CONGESTION_ACCESS_CLIENT_* variables.
func ConfigFromEnv() Config {
	return Config{
		URL:             os.Getenv("FPL_MCP_CONGESTION_URL"),
		AuthToken:       os.Getenv("FPL_MCP_CONGESTION_TOKEN"),
		AccessClientID:  os.Getenv("FPL_MCP_CONGESTION_ACCESS_CLIENT_ID"),
		AccessClientKey: os.Getenv("FPL_MCP_CONGESTION_ACCESS_CLIENT_SECRET"),
	}
}

// localTTL bounds how often Client re-fetches: the captured file only
// changes on fantasypl/data's own capture cadence (daily), so this is
// purely about not doing a network round trip on every single tool call
// within a server process's lifetime.
const localTTL = time.Hour

// timeout bounds the one HTTP call a fetch ever makes. Congestion is
// enrichment, never a hard dependency — an unreachable or slow endpoint
// must not delay a tool call.
const timeout = 5 * time.Second

// Client implements algo.CongestionSource against a captured calendar.
type Client struct {
	Config
	HTTP *http.Client

	mu       sync.Mutex
	cached   map[int][]time.Time
	cachedAt time.Time
	now      func() time.Time
}

// NewClient returns a Client for cfg. If cfg.URL is empty, TeamFixtureCalendar
// always returns an error immediately — callers should check cfg.URL
// themselves before wiring this in, the same way cmd/fpl-mcp checks
// remoteweights.ConfigFromEnv().URL.
func NewClient(cfg Config) *Client {
	return &Client{Config: cfg, HTTP: &http.Client{Timeout: timeout}, now: time.Now}
}

// TeamFixtureCalendar satisfies algo.CongestionSource. season/fromGW/toGW
// are accepted for interface compatibility but unused: the captured file is
// one JSON object for the whole competition, cheap enough to always fetch
// in full rather than slice by gameweek.
func (c *Client) TeamFixtureCalendar(ctx context.Context, _ string, _, _ int) (map[int][]time.Time, error) {
	if c.URL == "" {
		return nil, fmt.Errorf("remotecongestion: no URL configured")
	}

	c.mu.Lock()
	if c.cached != nil && c.now().Sub(c.cachedAt) < localTTL {
		defer c.mu.Unlock()
		return c.cached, nil
	}
	c.mu.Unlock()

	calendar, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cached, c.cachedAt = calendar, c.now()
	c.mu.Unlock()
	return calendar, nil
}

func (c *Client) fetch(ctx context.Context) (map[int][]time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("remotecongestion: build request: %w", err)
	}
	if c.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}
	if c.AccessClientID != "" {
		req.Header.Set("CF-Access-Client-Id", c.AccessClientID)
	}
	if c.AccessClientKey != "" {
		req.Header.Set("CF-Access-Client-Secret", c.AccessClientKey)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remotecongestion: fetch %s: %w", c.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remotecongestion: fetch %s: status %d", c.URL, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("remotecongestion: read %s: %w", c.URL, err)
	}

	// The captured file's keys are FPL team codes as JSON object keys
	// (strings, per JSON's own rules), values are ISO8601 kickoff times —
	// see fplctl congestion-capture in fantasypl/data for exactly what
	// writes this shape.
	var raw map[string][]time.Time
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("remotecongestion: parse %s: %w", c.URL, err)
	}
	calendar := make(map[int][]time.Time, len(raw))
	for k, v := range raw {
		code, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		calendar[code] = v
	}
	return calendar, nil
}
