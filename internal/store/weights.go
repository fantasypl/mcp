package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// OptimizedWeightsCache is data/optimized_weights.json: the result of the last
// weight-optimizer run, plus enough metadata to decide whether it's still
// fresh and to explain where it came from.
//
// Weights is a plain map, not algo.Weights, because the optimizer's base
// weight set is missing one key (news_penalty) that the scoring weights
// struct has — see algo.MergeWeights for why that's fine rather than a bug.
type OptimizedWeightsCache struct {
	Weights          map[string]float64 `json:"weights"`
	OptimizedAtEpoch float64            `json:"optimized_at_epoch"`
	BaseWeights      map[string]float64 `json:"base_weights"`
	RollingWindow    int                `json:"rolling_window"`
}

// Fresh reports whether the cache is younger than ttl as of now.
func (c OptimizedWeightsCache) Fresh(ttl time.Duration, now time.Time) bool {
	age := now.Sub(time.Unix(0, int64(c.OptimizedAtEpoch*float64(time.Second))))
	return age < ttl
}

// LoadOptimizedWeightsCache reads data/optimized_weights.json if present.
func (l Layout) LoadOptimizedWeightsCache() (*OptimizedWeightsCache, bool, error) {
	c, ok, err := readJSON[OptimizedWeightsCache](l.OptimizedWeightsPath())
	if !ok || err != nil {
		return nil, ok, err
	}
	return &c, true, nil
}

// SaveOptimizedWeightsCache writes the cache, creating data/ if needed.
// Indented because this file is meant to be readable by a human debugging why
// captaincy picks shifted.
func (l Layout) SaveOptimizedWeightsCache(c *OptimizedWeightsCache) error {
	path := l.OptimizedWeightsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
