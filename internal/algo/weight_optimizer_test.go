package algo

import (
	"math"
	"testing"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/store"
)

// optimizerFixtureLayout points at testdata/optimizer_fixture/data — a small
// synthetic season (8 gameweeks, 30 players, 6 teams, randomly generated with
// a fixed seed) built specifically to cross-check the optimizer, since no
// real finished gameweek exists yet this season. testdata/optimizer_expected_
// {weights,scores}.json were captured by running the Python optimizer over
// this exact fixture.
func optimizerFixtureLayout() store.Layout {
	return store.Layout{Root: testdataPath("optimizer_fixture")}
}

func TestOptimizeWeightsMatchesPython(t *testing.T) {
	want := loadJSON[map[string]float64](t, testdataPath("optimizer_expected_weights.json"))

	got, ok, err := OptimizeWeights(optimizerFixtureLayout(), RollingWindow)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("optimizer reported insufficient data against a fixture built to have enough")
	}

	if len(got) != len(want) {
		t.Fatalf("got %d weight keys, want %d", len(got), len(want))
	}
	for k, wv := range want {
		gv, present := got[k]
		if !present {
			t.Errorf("missing weight %q", k)
			continue
		}
		if math.Abs(gv-wv) > 1e-9 {
			t.Errorf("%s = %v, want %v", k, gv, wv)
		}
	}
}

// The search must actually find an improvement on this fixture — the Python
// run against the same data went from 61 to 67. If Go's search space or
// scoring diverges even slightly, this is usually the first thing to catch it
// because it's a single scalar rather than 15 floats to eyeball.
func TestOptimizeWeightsImprovesOnBaseline(t *testing.T) {
	type scores struct {
		BaseScore int `json:"base_score"`
		BestScore int `json:"best_score"`
	}
	want := loadJSON[scores](t, testdataPath("optimizer_expected_scores.json"))

	layout := optimizerFixtureLayout()
	fixturesData, ok, err := layout.LoadFixturesCache()
	if err != nil || !ok {
		t.Fatalf("load fixtures cache: ok=%v err=%v", ok, err)
	}

	var gws []int
	snapshots := map[int]*store.Snapshot{}
	liveData := map[int]*store.LiveData{}
	for gw := 23; gw <= 30; gw++ {
		snap, ok, err := layout.LoadSnapshot(gw)
		if err != nil || !ok {
			t.Fatalf("load snapshot gw%d: ok=%v err=%v", gw, ok, err)
		}
		live, ok, err := layout.LoadLiveData(gw)
		if err != nil || !ok {
			t.Fatalf("load live gw%d: ok=%v err=%v", gw, ok, err)
		}
		gws = append(gws, gw)
		snapshots[gw] = snap
		liveData[gw] = live
	}

	base := optimizerBaseWeights()
	baseScore := evaluateWeights(base, gws, snapshots, liveData, fixturesData)
	if baseScore != want.BaseScore {
		t.Errorf("base score = %d, want %d", baseScore, want.BaseScore)
	}

	best, ok, err := OptimizeWeights(layout, RollingWindow)
	if err != nil || !ok {
		t.Fatalf("OptimizeWeights: ok=%v err=%v", ok, err)
	}
	bestScore := evaluateWeights(best, gws, snapshots, liveData, fixturesData)
	if bestScore != want.BestScore {
		t.Errorf("best score = %d, want %d", bestScore, want.BestScore)
	}
	if bestScore < baseScore {
		t.Errorf("optimized score %d is worse than baseline %d", bestScore, baseScore)
	}
}

// No fixtures cache at all: OptimizeWeights must report "no data," not error.
func TestOptimizeWeightsNoFixturesCache(t *testing.T) {
	layout := store.Layout{Root: t.TempDir()}
	weights, ok, err := OptimizeWeights(layout, RollingWindow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || weights != nil {
		t.Errorf("expected (nil, false), got (%v, %v)", weights, ok)
	}
}

// Fewer than 3 qualifying gameweeks: same "no data" contract, this time
// exercised by pointing at a fixture with a fixtures cache but almost no
// paired snapshot/live data.
func TestOptimizeWeightsInsufficientGameweeks(t *testing.T) {
	root := t.TempDir()
	layout := store.Layout{Root: root}

	writeJSON(t, layout.FixturesCachePath(), []map[string]any{
		{"id": 1, "event": 1, "team_h": 1, "team_a": 2, "team_h_difficulty": 3, "team_a_difficulty": 3},
	})
	// Only one gameweek has both a snapshot and live data.
	writeJSON(t, layout.SnapshotPath(1), store.Snapshot{Gameweek: 1})
	writeJSON(t, layout.LiveDataPath(1), store.LiveData{})

	weights, ok, err := OptimizeWeights(layout, RollingWindow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || weights != nil {
		t.Errorf("expected insufficient-data result, got (%v, %v)", weights, ok)
	}
}

// A snapshot with no matching live data (gameweek not yet played) must be
// skipped, not treated as a zero-point captain pick.
func TestEvaluateWeightsSkipsUnmatchedGameweeks(t *testing.T) {
	layout := optimizerFixtureLayout()
	fixturesData, _, _ := layout.LoadFixturesCache()

	snap, _, err := layout.LoadSnapshot(23)
	if err != nil {
		t.Fatal(err)
	}
	live, _, err := layout.LoadLiveData(23)
	if err != nil {
		t.Fatal(err)
	}

	withData := evaluateWeights(optimizerBaseWeights(), []int{23},
		map[int]*store.Snapshot{23: snap}, map[int]*store.LiveData{23: live}, fixturesData)

	// GW 99 has a snapshot entry with no matching live data.
	noLive := evaluateWeights(optimizerBaseWeights(), []int{23, 99},
		map[int]*store.Snapshot{23: snap}, map[int]*store.LiveData{23: live}, fixturesData)

	if withData != noLive {
		t.Errorf("an unmatched gameweek changed the total: %d vs %d", withData, noLive)
	}
}

// GetOptimizedWeights must serve a fresh cache without re-running the search,
// and MergeWeights must land news_penalty at DefaultWeights' value (1.0) since
// the optimizer's own weight set never includes that key — see MergeWeights.
func TestGetOptimizedWeightsUsesFreshCache(t *testing.T) {
	layout := store.Layout{Root: t.TempDir()}
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)

	cache := &store.OptimizedWeightsCache{
		Weights:          map[string]float64{"form": 9.99},
		OptimizedAtEpoch: float64(now.Add(-10*time.Minute).UnixNano()) / 1e9,
		BaseWeights:      optimizerBaseWeights(),
		RollingWindow:    RollingWindow,
	}
	if err := layout.SaveOptimizedWeightsCache(cache); err != nil {
		t.Fatal(err)
	}

	got, used, err := GetOptimizedWeights(layout, now)
	if err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Fatal("expected the fresh cache to be used")
	}
	if got.Form != 9.99 {
		t.Errorf("Form = %v, want 9.99 (from cache)", got.Form)
	}
	if got.NewsPenalty != DefaultWeights().NewsPenalty {
		t.Errorf("NewsPenalty = %v, want the default %v (optimizer never sets this key)",
			got.NewsPenalty, DefaultWeights().NewsPenalty)
	}
}

// A stale cache must trigger a fresh search rather than being served as-is.
func TestGetOptimizedWeightsIgnoresStaleCache(t *testing.T) {
	root := t.TempDir()
	layout := store.Layout{Root: root}
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)

	stale := &store.OptimizedWeightsCache{
		Weights:          map[string]float64{"form": 0.01},
		OptimizedAtEpoch: float64(now.Add(-2*time.Hour).UnixNano()) / 1e9,
	}
	if err := layout.SaveOptimizedWeightsCache(stale); err != nil {
		t.Fatal(err)
	}
	// No fixtures cache present, so the fallback search finds no data and
	// GetOptimizedWeights must fall back to DefaultWeights entirely, not the
	// stale cached value.
	got, used, err := GetOptimizedWeights(layout, now)
	if err != nil {
		t.Fatal(err)
	}
	if used {
		t.Error("a stale cache with no data to re-optimize from should not report success")
	}
	if got != DefaultWeights() {
		t.Error("expected a clean fallback to DefaultWeights, not the stale value")
	}
}

// End to end: a cold cache directory pointed at the real fixture must run the
// search, persist a cache file, and have a second call read that cache back
// without re-running — proving the round trip through disk, not just memory.
func TestGetOptimizedWeightsPersistsAndReloads(t *testing.T) {
	root := t.TempDir()
	layout := store.Layout{Root: root}
	copyFixtureInto(t, testdataPath("optimizer_fixture"), root)
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)

	first, used, err := GetOptimizedWeights(layout, now)
	if err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Fatal("expected the search to find and apply optimized weights")
	}

	cache, ok, err := layout.LoadOptimizedWeightsCache()
	if err != nil || !ok {
		t.Fatalf("expected a persisted cache file: ok=%v err=%v", ok, err)
	}
	if !cache.Fresh(WeightsCacheTTL, now) {
		t.Error("just-written cache should be fresh")
	}

	second, used2, err := GetOptimizedWeights(layout, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !used2 || second != first {
		t.Errorf("second call = (%+v, %v), want identical result served from cache", second, used2)
	}
}
