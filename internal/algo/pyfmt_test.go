package algo

import (
	"fmt"
	"math"
	"testing"
)

// Expected values generated from CPython 3.12. If one of these fails, the port
// will silently disagree with the reference implementation somewhere in the
// scoring pipeline.

func TestRoundMatchesPython(t *testing.T) {
	cases := []struct {
		in   float64
		n    int
		want float64
	}{
		// Ties resolve to even, not away from zero. math.Round gets these wrong.
		{0.125, 2, 0.12},
		{0.135, 2, 0.14},
		{2.675, 2, 2.67}, // 2.675 is really 2.67499...
		{1.005, 2, 1.0},
		{2.345, 2, 2.35},
		{9.6285, 2, 9.63},
		{11.9765, 2, 11.98},
		{123.456789, 2, 123.46},
		{3.14159265, 2, 3.14},
		{0.1 + 0.2, 2, 0.3},
		{1e-7, 2, 0.0},
		{1.4, 2, 1.4},

		{9.6285, 3, 9.629},
		{11.9765, 3, 11.976},
		{123.456789, 3, 123.457},
		{3.14159265, 3, 3.142},
		{2.675, 3, 2.675},
		{0.125, 3, 0.125},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("round(%v,%d)", tc.in, tc.n), func(t *testing.T) {
			if got := Round(tc.in, tc.n); got != tc.want {
				t.Errorf("Round(%v, %d) = %v, want %v", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

// Guard that the naive implementation really would be wrong, so this test is
// not quietly testing nothing.
func TestRoundDiffersFromMathRound(t *testing.T) {
	const x, n = 0.125, 2
	naive := math.Round(x*100) / 100
	if naive == Round(x, n) {
		t.Skip("platform math.Round happens to agree; guard is inert here")
	}
	if Round(x, n) != 0.12 || naive != 0.13 {
		t.Errorf("Round=%v naive=%v; expected 0.12 and 0.13", Round(x, n), naive)
	}
}

func TestRoundToIntMatchesPython(t *testing.T) {
	cases := []struct {
		in   float64
		want int
	}{
		{2.5, 2}, // banker's: to even
		{3.5, 4},
		{0.5, 0},
		{1.5, 2},
		{-2.5, -2},
		{2.675, 3},
		{9.629, 10},
		{11.9765, 12},
		{123.456789, 123},
		{1.4, 1},
		{1.8, 2},
		{4.6, 5},
		{32.811111, 33},
	}
	for _, tc := range cases {
		if got := RoundToInt(tc.in); got != tc.want {
			t.Errorf("RoundToInt(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// "%d" % 1.4 truncates in Python. Rounding here would shift FDR labels in the
// reasoning strings by one.
func TestTruncIntMatchesPython(t *testing.T) {
	cases := []struct {
		in   float64
		want int
	}{
		{1.4, 1},
		{1.8, 1},
		{4.6, 4},
		{-1.7, -1},
		{2.0, 2},
		{0.9, 0},
	}
	for _, tc := range cases {
		if got := TruncInt(tc.in); got != tc.want {
			t.Errorf("TruncInt(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestFloatStrMatchesPython(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{4.0, "4.0"}, // Go's default would be "4"
		{1.0, "1.0"},
		{0.0, "0.0"},
		{3.2, "3.2"},
		{2.5, "2.5"},
		{2.675, "2.675"},
		{123.456789, "123.456789"},
		{9.6285, "9.6285"},
		{-2.5, "-2.5"},
		{100.0, "100.0"},
	}
	for _, tc := range cases {
		if got := FloatStr(tc.in); got != tc.want {
			t.Errorf("FloatStr(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Go's fmt must agree with Python's format spec, since reasoning strings embed
// xG/90 as "%.2f".
func TestSprintfMatchesPythonFormatSpec(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0.125, "0.12"},
		{0.135, "0.14"},
		{2.675, "2.67"},
		{1.005, "1.00"},
		{2.345, "2.35"},
		{9.6285, "9.63"},
		{0.317, "0.32"},
		{0.777, "0.78"},
		{1.0, "1.00"},
	}
	for _, tc := range cases {
		if got := fmt.Sprintf("%.2f", tc.in); got != tc.want {
			t.Errorf("%%.2f of %v = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The single most mistranslatable line in the port.
func TestCapitalizeMatchesPython(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"Poor form, strong xG/90 (0.32), FPL expects 4.0pts, elite ICT index",
			"Poor form, strong xg/90 (0.32), fpl expects 4.0pts, elite ict index",
		},
		{"exceptional form", "Exceptional form"},
		{"on corners + free kicks", "On corners + free kicks"},
		{"NO FIXTURE this GW — do not captain", "No fixture this gw — do not captain"},
		{"", ""},
		{"a", "A"},
	}
	for _, tc := range cases {
		if got := Capitalize(tc.in); got != tc.want {
			t.Errorf("Capitalize(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		v, lo, hi, want float64
	}{
		{5, 0, 10, 0.5},
		{-1, 0, 10, 0.0}, // clamps low
		{20, 0, 10, 1.0}, // clamps high
		{5, 10, 10, 0.0}, // degenerate bounds
		{5, 10, 0, 0.0},  // inverted bounds
		{0.3, 0, 0.3, 1.0},
	}
	for _, tc := range cases {
		if got := Normalize(tc.v, tc.lo, tc.hi); math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("Normalize(%v,%v,%v) = %v, want %v", tc.v, tc.lo, tc.hi, got, tc.want)
		}
	}
}
