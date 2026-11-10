package calibration

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/vaastav"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSpearman(t *testing.T) {
	cases := []struct {
		name   string
		xs, ys []float64
		want   float64
		ok     bool
	}{
		{"perfect", []float64{1, 2, 3, 4}, []float64{10, 20, 30, 40}, 1, true},
		{"reversed", []float64{1, 2, 3, 4}, []float64{4, 3, 2, 1}, -1, true},
		// Ties take the average rank: xs ranks 1.5, 1.5, 3, 4.
		{"ties", []float64{1, 1, 2, 3}, []float64{1, 2, 3, 4}, 0.9486832980505138, true},
		{"monotone, not linear", []float64{1, 2, 3, 4, 5}, []float64{1, 4, 9, 16, 1000}, 1, true},
		{"a constant has no correlation", []float64{2, 2, 2}, []float64{1, 2, 3}, 0, false},
		{"too few points", []float64{1, 2}, []float64{1, 2}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Spearman(tc.xs, tc.ys)
			if ok != tc.ok || (ok && !near(got, tc.want)) {
				t.Errorf("Spearman = %v, %v; want %v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestComputeSplitsByPositionWithSampleSizes(t *testing.T) {
	var samples []Sample
	// DEF: form predicts points perfectly. MID: form predicts them in
	// reverse. Ten of each.
	for i := 0; i < 10; i++ {
		samples = append(samples,
			Sample{Position: "DEF", Signals: map[string]float64{"form": float64(i)}, NextPoints: i},
			Sample{Position: "MID", Signals: map[string]float64{"form": float64(i)}, NextPoints: 10 - i},
		)
	}
	r := Compute(samples, []SignalDef{{Name: "form"}})
	if len(r.Signals) != 1 {
		t.Fatalf("got %d signals, want 1", len(r.Signals))
	}
	s := r.Signals[0]
	if s.Overall.N != 20 {
		t.Errorf("overall n = %d, want 20", s.Overall.N)
	}
	def, mid := s.ByPosition["DEF"], s.ByPosition["MID"]
	if def.N != 10 || def.Rho == nil || !near(*def.Rho, 1) {
		t.Errorf("DEF = %+v, want n 10, rho 1", def)
	}
	if mid.N != 10 || mid.Rho == nil || !near(*mid.Rho, -1) {
		t.Errorf("MID = %+v, want n 10, rho -1", mid)
	}
	if _, ok := s.ByPosition["FWD"]; !ok || s.ByPosition["FWD"].N != 0 || s.ByPosition["FWD"].Rho != nil {
		t.Errorf("FWD = %+v, want an empty row with n 0 and no rho", s.ByPosition["FWD"])
	}
	// Ten samples is far too few to trust: the interval says so.
	if def.CI95[0] > 0.9 && mid.Reliability != "low" {
		t.Errorf("small samples should read as low reliability: DEF %+v MID %+v", def, mid)
	}
}

func TestStatConfidenceIntervalNarrowsWithSampleSize(t *testing.T) {
	small, large := statFor(0.3, 30), statFor(0.3, 3000)
	if small.CI95[1]-small.CI95[0] <= large.CI95[1]-large.CI95[0] {
		t.Errorf("CI at n 30 %v is not wider than at n 3000 %v", small.CI95, large.CI95)
	}
	if small.CI95[0] > 0.3 || small.CI95[1] < 0.3 {
		t.Errorf("CI %v does not contain rho 0.3", small.CI95)
	}
	if large.Reliability != "high" || small.Reliability != "low" {
		t.Errorf("reliability = %q at 3000 and %q at 30, want high and low", large.Reliability, small.Reliability)
	}
}

// The recorded 2025-26 GW29 backtest case: state through GW28, GW29 results.
func recordedCase(t *testing.T) *vaastav.Case {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "backtest", "gw29")
	read := func(name string, v any) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	var bootstrap fpl.Bootstrap
	var fixtures []fpl.Fixture
	var actualRaw map[string]struct {
		Points  int `json:"points"`
		Minutes int `json:"minutes"`
	}
	read("bootstrap_gw28.json", &bootstrap)
	read("fixtures.json", &fixtures)
	read("actual_gw29.json", &actualRaw)
	actual := make(map[int]vaastav.ActualEntry, len(actualRaw))
	for k, v := range actualRaw {
		id, _ := strconv.Atoi(k)
		actual[id] = vaastav.ActualEntry{Points: v.Points, Minutes: v.Minutes}
	}
	return &vaastav.Case{Season: "2025-26", PredictGW: 29, Bootstrap: &bootstrap, Fixtures: fixtures, Actual: actual}
}

func TestSamplesFromCase(t *testing.T) {
	c := recordedCase(t)
	samples, err := SamplesFromCase(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) < 200 {
		t.Fatalf("got %d samples, want the regulars of a full gameweek", len(samples))
	}
	for _, s := range samples {
		for _, sig := range DefaultSignals {
			if _, ok := s.Signals[sig.Name]; !ok {
				t.Fatalf("sample %+v is missing signal %s", s, sig.Name)
			}
		}
		if s.Position == "" || s.Position == "?" {
			t.Fatalf("sample %+v has no position", s)
		}
	}

	r := Compute(samples, DefaultSignals)
	for _, s := range r.Signals {
		if s.Overall.N != len(samples) || s.Overall.Rho == nil {
			t.Errorf("%s overall = %+v, want n %d and a rho", s.Name, s.Overall, len(samples))
		}
	}
}
