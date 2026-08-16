package algo

import (
	"math"
	"strconv"
	"strings"
)

// Python compatibility helpers.
//
// The port has to reproduce Python's output byte for byte, and several of
// Python's numeric and string defaults differ from Go's in ways that are
// invisible until a golden file fails. Each function here exists because the
// naive Go equivalent is wrong; every one is pinned by a test against values
// generated from CPython.

// Round reproduces Python's round(x, n): round-half-to-even on the decimal
// representation of the binary value.
//
// Go's math.Round is half-away-from-zero, so it disagrees on ties —
// round(0.125, 2) is 0.12 in Python and would be 0.13 via math.Round. Going
// through strconv gives correct decimal rounding with ties-to-even, which is
// what CPython does.
func Round(x float64, n int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	f, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', n, 64), 64)
	if err != nil {
		return x
	}
	return f
}

// RoundToInt reproduces Python's single-argument round(x), which returns an int
// using banker's rounding: round(2.5) is 2, round(3.5) is 4, round(0.5) is 0.
func RoundToInt(x float64) int {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return int(math.RoundToEven(x))
}

// TruncInt reproduces Python's "%d" % someFloat, which truncates toward zero
// rather than rounding. The reasoning strings rely on this: an FDR of 1.4
// renders as "FDR 1", and 4.6 renders as "FDR 4", not 5.
func TruncInt(x float64) int {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return int(x)
}

// FloatStr reproduces Python's str(float) / f"{v}".
//
// Python always renders a decimal point for an integral float — str(4.0) is
// "4.0" — while Go's shortest formatting yields "4". The reasoning strings
// interpolate expected points directly ("FPL expects 4.0pts"), so the trailing
// ".0" is load-bearing.
func FloatStr(f float64) string {
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	if math.IsNaN(f) {
		return "nan"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	// Go writes exponents as "1e-07"/"1e+16"; Python writes "1e-07"/"1e+16"
	// too, so no adjustment is needed for the ranges these algorithms produce.
	return s
}

// Capitalize reproduces Python's str.capitalize(): upper-case the first
// character and lower-case *everything else*.
//
// This is the one most likely to be mistranslated. strings.ToUpper on the first
// rune alone leaves "xG/90" and "FPL" intact, but Python emits "xg/90" and
// "fpl" — which is exactly what the golden reasoning strings contain.
func Capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	head := strings.ToUpper(string(r[0]))
	tail := strings.ToLower(string(r[1:]))
	return head + tail
}

// Normalize scales value into [0,1] against the given bounds, clamping.
// Mirrors captain._normalize.
func Normalize(value, low, high float64) float64 {
	if high <= low {
		return 0.0
	}
	return math.Max(0.0, math.Min(1.0, (value-low)/(high-low)))
}
