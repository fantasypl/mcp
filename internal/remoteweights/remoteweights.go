// Package remoteweights fetches a centrally computed
// optimized_weights.json from a private, Cloudflare Access-gated endpoint —
// for the maintainer's own fpl-mcp instance only. It is entirely inert
// unless FPL_MCP_WEIGHTS_URL is set: every public `go install` user runs
// with algo.DefaultWeights(), exactly as before this package existed.
package remoteweights

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fantasypl/mcp/internal/algo"
	"github.com/fantasypl/mcp/internal/store"
)

// Config points Load at the Cloudflare Access-gated Worker serving the
// current optimized_weights.json. All three fields come from environment
// variables that are unset for every install but the maintainer's own — an
// empty URL is the feature's off switch.
type Config struct {
	URL             string
	AccessClientID  string
	AccessClientKey string
}

// ConfigFromEnv reads FPL_MCP_WEIGHTS_URL, FPL_MCP_WEIGHTS_ACCESS_CLIENT_ID,
// and FPL_MCP_WEIGHTS_ACCESS_CLIENT_SECRET, trimming surrounding whitespace
// (a value pasted into an MCP client's user_config with a leading space
// would otherwise fail URL parsing and silently fall back to defaults).
func ConfigFromEnv() Config {
	return Config{
		URL:             strings.TrimSpace(os.Getenv("FPL_MCP_WEIGHTS_URL")),
		AccessClientID:  strings.TrimSpace(os.Getenv("FPL_MCP_WEIGHTS_ACCESS_CLIENT_ID")),
		AccessClientKey: strings.TrimSpace(os.Getenv("FPL_MCP_WEIGHTS_ACCESS_CLIENT_SECRET")),
	}
}

// localTTL bounds how often Load re-fetches over the network. The Worker's
// content only ever changes on redeploy (see fantasypl/data's capture-and-
// deploy pipeline), so this isn't tracking upstream freshness — it's purely
// to avoid a network round trip on every single server start within a short
// window, matching the field's existing meaning in
// store.OptimizedWeightsCache ("how old is this cached file").
const localTTL = 10 * time.Minute

// timeout bounds the one HTTP call Load ever makes. Weights are an
// enhancement, never a hard dependency — an unreachable or slow endpoint
// must not delay server startup.
const timeout = 5 * time.Second

// Load returns the weights to use and whether a remote or locally-cached
// value was applied. It never returns an error to the caller: every failure
// mode (no config, network error, auth failure, bad JSON) logs one line and
// falls back to algo.DefaultWeights(), false — the same convention
// algo.GetOptimizedWeights already uses for its local-optimizer path.
func Load(ctx context.Context, cfg Config, layout store.Layout, now time.Time) (algo.Weights, bool) {
	if cfg.URL == "" {
		return algo.DefaultWeights(), false
	}

	if cache, ok, err := layout.LoadOptimizedWeightsCache(); err == nil && ok && cache.Fresh(localTTL, now) {
		return algo.MergeWeights(algo.DefaultWeights(), cache.Weights), true
	}

	cache, err := fetch(ctx, cfg)
	if err != nil {
		log.Printf("remoteweights: %v; using default weights", err)
		return algo.DefaultWeights(), false
	}

	computedAt := time.Unix(0, int64(cache.OptimizedAtEpoch*float64(time.Second)))
	cache.OptimizedAtEpoch = float64(now.UnixNano()) / 1e9 // local-cache freshness, not upstream compute time
	if err := layout.SaveOptimizedWeightsCache(cache); err != nil {
		log.Printf("remoteweights: cache fetched weights: %v", err)
	}
	log.Printf("remoteweights: applied weights computed %s", computedAt.UTC().Format(time.RFC3339))
	return algo.MergeWeights(algo.DefaultWeights(), cache.Weights), true
}

func fetch(ctx context.Context, cfg Config) (*store.OptimizedWeightsCache, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if cfg.AccessClientID != "" {
		req.Header.Set("CF-Access-Client-Id", cfg.AccessClientID)
	}
	if cfg.AccessClientKey != "" {
		req.Header.Set("CF-Access-Client-Secret", cfg.AccessClientKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", cfg.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %d", cfg.URL, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", cfg.URL, err)
	}

	var cache store.OptimizedWeightsCache
	if err := json.Unmarshal(b, &cache); err != nil {
		return nil, fmt.Errorf("parse %s: %w", cfg.URL, err)
	}
	return &cache, nil
}
