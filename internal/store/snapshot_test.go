package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

func TestLayoutPaths(t *testing.T) {
	l := Layout{Root: "/proj"}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"snapshot", l.SnapshotPath(7), "/proj/data/snapshots/gw7.json"},
		{"live", l.LiveDataPath(7), "/proj/data/backtest_cache/live_gw7.json"},
		{"fixtures", l.FixturesCachePath(), "/proj/data/backtest_cache/fixtures.json"},
		{"weights", l.OptimizedWeightsPath(), "/proj/data/optimized_weights.json"},
	}
	for _, tc := range cases {
		if filepath.ToSlash(tc.got) != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// A missing file is "not available yet," not an error — every caller in this
// package (and the optimizer built on top of it) depends on that distinction
// to treat an unplayed gameweek as routine rather than a fault.
func TestLoadersReportMissingFileWithoutError(t *testing.T) {
	l := Layout{Root: t.TempDir()}

	if snap, ok, err := l.LoadSnapshot(1); err != nil || ok || snap != nil {
		t.Errorf("LoadSnapshot: got (%v, %v, %v), want (nil, false, nil)", snap, ok, err)
	}
	if live, ok, err := l.LoadLiveData(1); err != nil || ok || live != nil {
		t.Errorf("LoadLiveData: got (%v, %v, %v), want (nil, false, nil)", live, ok, err)
	}
	if fx, ok, err := l.LoadFixturesCache(); err != nil || ok || fx != nil {
		t.Errorf("LoadFixturesCache: got (%v, %v, %v), want (nil, false, nil)", fx, ok, err)
	}
	if c, ok, err := l.LoadOptimizedWeightsCache(); err != nil || ok || c != nil {
		t.Errorf("LoadOptimizedWeightsCache: got (%v, %v, %v), want (nil, false, nil)", c, ok, err)
	}
}

// Malformed JSON, by contrast, must surface as a real error — silently
// treating a corrupt snapshot as "missing" would make the optimizer think a
// gameweek simply wasn't captured, rather than that its data is broken.
func TestLoadersReturnErrorOnCorruptFile(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	path := l.SnapshotPath(1)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, ok, err := l.LoadSnapshot(1)
	if err == nil {
		t.Fatal("expected a decode error")
	}
	if ok || snap != nil {
		t.Errorf("got (%v, %v) alongside an error, want (nil, false)", snap, ok)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	l := Layout{Root: t.TempDir()}

	chance := 75
	penOrder := 1
	original := Snapshot{
		Gameweek:   12,
		CapturedAt: "2026-03-10T12:00:00Z",
		IsBackfill: false,
		Event:      SnapshotEvent{ID: ptr(12), DeadlineTime: "2026-03-10T18:30:00Z", Finished: true, DataChecked: true},
		Players: []SnapshotPlayer{
			{
				ID: 411, WebName: "Haaland", Team: 11, ElementType: 4,
				Form: numFromJSON(t, `"5.4"`), PointsPerGame: numFromJSON(t, `"6.8"`),
				TotalPoints: 239, Minutes: 2953, Starts: 34,
				ExpectedGoals: numFromJSON(t, `"25.5"`),
				Status:        "a", ChanceOfPlayingNextRound: &chance,
				PenaltiesOrder: &penOrder,
			},
		},
		Teams: []SnapshotTeam{
			{ID: 11, Name: "Man City", ShortName: "MCI", StrengthAttackHome: 1350},
		},
		FixtureCount: 1,
	}

	path := l.SnapshotPath(12)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok, err := l.LoadSnapshot(12)
	if err != nil || !ok {
		t.Fatalf("LoadSnapshot: ok=%v err=%v", ok, err)
	}
	if got.Gameweek != 12 || len(got.Players) != 1 || got.Players[0].WebName != "Haaland" {
		t.Errorf("round trip lost data: %+v", got)
	}
	if got.Players[0].ChanceOfPlayingNextRound == nil || *got.Players[0].ChanceOfPlayingNextRound != 75 {
		t.Errorf("chance_of_playing_next_round did not round-trip: %+v", got.Players[0].ChanceOfPlayingNextRound)
	}
	if got.Players[0].PenaltiesOrder == nil || *got.Players[0].PenaltiesOrder != 1 {
		t.Errorf("penalties_order did not round-trip: %+v", got.Players[0].PenaltiesOrder)
	}
}

// The distinction that matters throughout this codebase: a nil
// chance_of_playing means "no flag raised," and 0 means "definitely out."
// The snapshot format must preserve that distinction on disk exactly like the
// live client does.
func TestSnapshotPlayerPreservesNilVsZeroChance(t *testing.T) {
	zero := 0
	raw := `[{"id":1,"chance_of_playing_next_round":null},{"id":2,"chance_of_playing_next_round":0}]`
	var players []SnapshotPlayer
	if err := json.Unmarshal([]byte(raw), &players); err != nil {
		t.Fatal(err)
	}
	if players[0].ChanceOfPlayingNextRound != nil {
		t.Error("player 1: expected nil chance, got a value")
	}
	if players[1].ChanceOfPlayingNextRound == nil || *players[1].ChanceOfPlayingNextRound != zero {
		t.Error("player 2: expected chance 0, got nil or a different value")
	}
}

func TestLiveDataActualPoints(t *testing.T) {
	raw := `{"elements":[{"id":1,"stats":{"total_points":12}},{"id":2,"stats":{"total_points":-1}}]}`
	var live LiveData
	if err := json.Unmarshal([]byte(raw), &live); err != nil {
		t.Fatal(err)
	}
	pts := live.ActualPoints()
	if pts[1] != 12 || pts[2] != -1 {
		t.Errorf("got %v, want {1:12, 2:-1}", pts)
	}
	if _, ok := pts[999]; ok {
		t.Error("unexpected entry for a player not in the data")
	}
}

// The fixtures cache is byte-identical to the live /fixtures/ endpoint shape,
// so it must decode into the same fpl.Fixture type the live client uses.
func TestFixturesCacheDecodesAsLiveFixtureType(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	gw := 5
	fixtures := []fpl.Fixture{
		{ID: 1, Event: &gw, TeamH: 1, TeamA: 2, TeamHDifficulty: 3, TeamADifficulty: 2},
	}
	path := l.FixturesCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(fixtures)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok, err := l.LoadFixturesCache()
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(got) != 1 || !got[0].InGameweek(5) {
		t.Errorf("got %+v", got)
	}
}

func TestOptimizedWeightsCacheFreshness(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		age  time.Duration
		want bool
	}{
		{"just written", 0, true},
		{"within an hour", 59 * time.Minute, true},
		{"exactly at the boundary", time.Hour, false},
		{"stale", 2 * time.Hour, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := OptimizedWeightsCache{OptimizedAtEpoch: float64(now.Add(-tc.age).UnixNano()) / 1e9}
			if got := c.Fresh(time.Hour, now); got != tc.want {
				t.Errorf("Fresh() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSaveOptimizedWeightsCacheCreatesDataDir(t *testing.T) {
	root := t.TempDir()
	l := Layout{Root: root}

	cache := &OptimizedWeightsCache{
		Weights:          map[string]float64{"form": 1.5},
		OptimizedAtEpoch: 1_700_000_000,
		BaseWeights:      map[string]float64{"form": 3.43},
		RollingWindow:    8,
	}
	if err := l.SaveOptimizedWeightsCache(cache); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(l.OptimizedWeightsPath()); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}

	got, ok, err := l.LoadOptimizedWeightsCache()
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Weights["form"] != 1.5 || got.RollingWindow != 8 {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

// The cache file is meant to be read by a human debugging a captaincy shift,
// so it must be indented, not minified.
func TestSaveOptimizedWeightsCacheIsIndented(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	if err := l.SaveOptimizedWeightsCache(&OptimizedWeightsCache{Weights: map[string]float64{"form": 1}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(l.OptimizedWeightsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !containsNewlineIndent(b) {
		t.Errorf("expected indented JSON, got: %s", b)
	}
}

func containsNewlineIndent(b []byte) bool {
	for i := 0; i+2 < len(b); i++ {
		if b[i] == '\n' && (b[i+1] == ' ' || b[i+1] == '\t') {
			return true
		}
	}
	return false
}

func numFromJSON(t *testing.T, raw string) fpl.Num {
	t.Helper()
	var n fpl.Num
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func ptr[T any](v T) *T { return &v }
