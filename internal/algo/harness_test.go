package algo

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

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
