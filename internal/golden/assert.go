package golden

import "testing"

// Assert compares a Go algorithm result against a golden file and fails the
// test with located, readable mismatches.
//
// Usage from an algorithm test:
//
//	got, err := eng.CaptainPicks(ctx, ptr(1))
//	golden.Assert(t, "../../testdata/golden/captain_gw1.json", got)
func Assert(t testing.TB, path string, actual any) {
	t.Helper()

	want, err := Load(path)
	if err != nil {
		t.Fatalf("load golden: %v", err)
	}
	got, err := Normalize(actual)
	if err != nil {
		t.Fatalf("normalize actual: %v", err)
	}
	if ms := Diff(want, got, Epsilon); len(ms) > 0 {
		t.Errorf("output does not match %s\n%s", path, Format(ms, 25))
	}
}
