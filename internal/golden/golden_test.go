package golden

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// The harness is the only thing standing between a subtle implementation bug and a
// silently wrong recommendation, so it gets tested before it is trusted.

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return v
}

func TestDiffDetects(t *testing.T) {
	cases := []struct {
		name       string
		want, got  string
		wantIssues int
		wantPath   string
	}{
		{"identical", `{"a":1,"b":"x"}`, `{"a":1,"b":"x"}`, 0, ""},

		// Float drift below Epsilon is expected across runtimes and
		// must not fail; drift above it must.
		{"float within epsilon", `{"score":9.629}`, `{"score":9.6290000001}`, 0, ""},
		{"float beyond epsilon", `{"score":9.629}`, `{"score":9.63}`, 1, "$.score"},

		// The trap from the plan: chance_of_playing_next_round is null for a
		// fit player and 0 for one who is definitely out. Collapsing them
		// inverts the injury penalty, so it must be caught.
		{"null vs zero", `{"chance":null}`, `{"chance":0}`, 1, "$.chance"},
		{"zero vs null", `{"chance":0}`, `{"chance":null}`, 1, "$.chance"},
		{"null vs null", `{"chance":null}`, `{"chance":null}`, 0, ""},

		// FPL emits form as the string "6.2"; code that helpfully converts
		// it to a number changes the output shape.
		{"string vs number", `{"form":"6.2"}`, `{"form":6.2}`, 1, "$.form"},

		{"missing key", `{"a":1,"b":2}`, `{"a":1}`, 1, "$.b"},
		{"unexpected key", `{"a":1}`, `{"a":1,"b":2}`, 1, "$.b"},
		{"array length", `{"p":[1,2,3]}`, `{"p":[1,2]}`, 1, "$.p"},
		{"array element", `{"p":[1,2,3]}`, `{"p":[1,2,4]}`, 1, "$.p[2]"},
		{"nested path", `{"a":{"b":{"c":1}}}`, `{"a":{"b":{"c":2}}}`, 1, "$.a.b.c"},
		{"string differs", `{"r":"Poor form"}`, `{"r":"Strong form"}`, 1, "$.r"},
		{"bool differs", `{"dgw":false}`, `{"dgw":true}`, 1, "$.dgw"},
		{"object vs array", `{"a":{}}`, `{"a":[]}`, 1, "$.a"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := Diff(decode(t, tc.want), decode(t, tc.got), Epsilon)
			if len(ms) != tc.wantIssues {
				t.Fatalf("got %d mismatches, want %d: %s",
					len(ms), tc.wantIssues, Format(ms, 10))
			}
			if tc.wantPath != "" && ms[0].Path != tc.wantPath {
				t.Errorf("path = %q, want %q", ms[0].Path, tc.wantPath)
			}
		})
	}
}

// A large all-different payload must not produce an unreadable failure.
func TestFormatCaps(t *testing.T) {
	want := map[string]any{}
	got := map[string]any{}
	for i := 0; i < 100; i++ {
		k := string(rune('a' + i%26))
		want[k+string(rune('0'+i/26))] = float64(i)
		got[k+string(rune('0'+i/26))] = float64(i + 1)
	}
	w, _ := Normalize(want)
	g, _ := Normalize(got)
	ms := Diff(w, g, Epsilon)
	if len(ms) != 100 {
		t.Fatalf("expected 100 mismatches, got %d", len(ms))
	}
	out := Format(ms, 5)
	if n := len(splitLines(out)); n > 8 {
		t.Errorf("Format did not cap output: %d lines", n)
	}
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// Every committed golden file must be loadable and non-trivial. This catches a
// truncated or accidentally-emptied fixture before it silently "passes" an
// algorithm test that produces nothing.
func TestGoldenFilesAreUsable(t *testing.T) {
	paths, err := filepath.Glob("../../testdata/golden/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no golden files found — run `fplctl gengolden`")
	}
	for _, p := range paths {
		v, err := Load(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			t.Errorf("%s: top level is not an object", p)
			continue
		}
		if len(m) == 0 {
			t.Errorf("%s: empty object", p)
		}
		// A golden file compared against itself must always be clean;
		// otherwise the comparator has a self-consistency bug.
		if ms := Diff(v, v, Epsilon); len(ms) > 0 {
			t.Errorf("%s: not self-consistent: %s", p, Format(ms, 5))
		}
	}
}
