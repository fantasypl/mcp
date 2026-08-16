package algo

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

// stubClient serves the frozen payloads in testdata/, so algorithm tests are
// offline and deterministic — the same inputs scripts/gen_golden.py fed to the
// Python implementation.
//
// picks is keyed by (teamID, gw) so a single stub can serve the synthetic
// squad fixtures used by the team-dependent algorithms — see the port plan's
// note on the entry/ data problem for why these are synthetic rather than
// real historical picks.
type stubClient struct {
	bootstrap   *fpl.Bootstrap
	fixtures    []fpl.Fixture
	picks       map[picksKey]*fpl.TeamPicks
	live        map[int]*fpl.LiveResponse
	eventStatus *fpl.EventStatusResponse
	// history and leagues follow the same "absent = 404" convention as
	// picks: a missing map entry simulates that manager's fetch failing,
	// which is exactly the partial-failure case league_analyzer has to
	// handle per manager without aborting the whole request.
	history   map[int]*fpl.TeamHistory
	leagues   map[int]*fpl.LeagueStandings
	transfers map[int][]fpl.ManagerTransfer
	// playerSummaries follows the same "absent = 404" convention, keyed by
	// player id — compare.go's per-player element-summary lookups.
	playerSummaries map[int]*fpl.PlayerSummary
}

type picksKey struct {
	teamID, gw int
}

func (s *stubClient) Bootstrap(context.Context) (*fpl.Bootstrap, error) { return s.bootstrap, nil }
func (s *stubClient) Fixtures(context.Context) ([]fpl.Fixture, error)   { return s.fixtures, nil }

func (s *stubClient) TeamPicks(_ context.Context, teamID, gw int) (*fpl.TeamPicks, error) {
	p, ok := s.picks[picksKey{teamID, gw}]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return p, nil
}

func (s *stubClient) LivePoints(_ context.Context, gw int) (*fpl.LiveResponse, error) {
	l, ok := s.live[gw]
	if !ok {
		return &fpl.LiveResponse{}, nil
	}
	return l, nil
}

func (s *stubClient) EventStatus(context.Context) (*fpl.EventStatusResponse, error) {
	if s.eventStatus == nil {
		return &fpl.EventStatusResponse{}, nil
	}
	return s.eventStatus, nil
}

func (s *stubClient) TeamHistory(_ context.Context, teamID int) (*fpl.TeamHistory, error) {
	h, ok := s.history[teamID]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return h, nil
}

func (s *stubClient) LeagueStandings(_ context.Context, leagueID int) (*fpl.LeagueStandings, error) {
	l, ok := s.leagues[leagueID]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return l, nil
}

func (s *stubClient) ManagerTransfers(_ context.Context, teamID int) ([]fpl.ManagerTransfer, error) {
	t, ok := s.transfers[teamID]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return t, nil
}

func (s *stubClient) PlayerSummary(_ context.Context, playerID int) (*fpl.PlayerSummary, error) {
	ps, ok := s.playerSummaries[playerID]
	if !ok {
		return nil, &fpl.HTTPError{StatusCode: 404, URL: "stub"}
	}
	return ps, nil
}

func testdataPath(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", "testdata"}, parts...)...)
}

func loadJSON[T any](t *testing.T, path string) T {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return v
}

// goldenClock is pinned to the date the golden files were generated. News age
// is rendered relatively ("2 days ago"), so an unpinned clock would make any
// case involving an injured player fail a day later.
var goldenClock = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

// newEngine builds an Engine over the named bootstrap fixture.
//
// "preseason" is the live payload: last season's totals carried over, form
// reset to 0.0 for every player. "midseason" injects real form, which is the
// only way to exercise the form term and detect_streak.
func newEngine(t *testing.T, fixture string) *Engine {
	t.Helper()
	c := &stubClient{
		bootstrap: loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_"+fixture+".json")),
		fixtures:  loadJSON[[]fpl.Fixture](t, testdataPath("fixtures.json")),
	}
	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }
	return e
}

func goldenPath(name string) string { return testdataPath("golden", name+".json") }

// writeJSON marshals v to path, creating parent directories as needed. Used
// by tests that construct a store.Layout fixture from scratch rather than
// reading one from testdata/.
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// copyFixtureInto copies a testdata fixture directory tree into dst, so a
// test can exercise on-disk read/write round-trips (e.g. GetOptimizedWeights
// persisting a cache file) without mutating the checked-in fixture.
func copyFixtureInto(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
	if err != nil {
		t.Fatalf("copy fixture %s -> %s: %v", src, dst, err)
	}
}

// bothFixtures runs fn against each bootstrap fixture, passing the golden-file
// suffix the generator used ("" for preseason, "_mid" for midseason).
func bothFixtures(t *testing.T, fn func(t *testing.T, e *Engine, suffix string)) {
	t.Helper()
	for _, tc := range []struct{ fixture, suffix string }{
		{"preseason", ""},
		{"midseason", "_mid"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			fn(t, newEngine(t, tc.fixture), tc.suffix)
		})
	}
}
