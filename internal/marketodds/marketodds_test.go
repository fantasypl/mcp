package marketodds

import (
	"errors"
	"math"
	"testing"
)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.4f, want %.4f ± %.4f", name, got, want, tol)
	}
}

func TestDevigSumsToOne(t *testing.T) {
	pr, err := Devig(Prices{Home: 1.30, Draw: 6.0, Away: 9.5, Over25: 1.36, Under25: 3.2})
	if err != nil {
		t.Fatal(err)
	}
	near(t, "1X2 sum", pr.Home+pr.Draw+pr.Away, 1, 1e-9)
	if !pr.HasTotals {
		t.Fatal("expected totals market to be detected")
	}
	if pr.Home < pr.Away {
		t.Errorf("heavy favourite should have Home > Away: %+v", pr)
	}
}

func TestDevigRejectsBadPrices(t *testing.T) {
	for _, p := range []Prices{{}, {Home: 2, Draw: 3}, {Home: 0.9, Draw: 3, Away: 3}} {
		if _, err := Devig(p); !errors.Is(err, ErrNoPrices) {
			t.Errorf("Devig(%+v) err = %v, want ErrNoPrices", p, err)
		}
	}
}

// Round trip: pick goal rates, generate the probabilities they imply, and
// check Fit recovers them.
func TestFitRecoversKnownRates(t *testing.T) {
	for _, tc := range []struct{ lh, la float64 }{{2.1, 0.7}, {1.3, 1.1}, {0.9, 1.8}} {
		h, d, a, over := outcome(tc.lh, tc.la)
		m := Fit(Probs{Home: h, Draw: d, Away: a, Over25: over, HasTotals: true})
		near(t, "home xG", m.HomeXG, tc.lh, 0.03)
		near(t, "away xG", m.AwayXG, tc.la, 0.03)
		near(t, "home CS", m.HomeCS, math.Exp(-tc.la), 0.02)
	}
}

func TestFitWithoutTotalsStaysSane(t *testing.T) {
	m, err := Model(Prices{Home: 1.5, Draw: 4.2, Away: 6.5})
	if err != nil {
		t.Fatal(err)
	}
	near(t, "total goals", m.HomeXG+m.AwayXG, defaultTotalGoals, 0.5)
	if m.HomeCS <= m.AwayCS {
		t.Errorf("favourite should be likelier to keep a clean sheet: home %.2f away %.2f", m.HomeCS, m.AwayCS)
	}
}

func TestCleanSheetIsProbability(t *testing.T) {
	m, err := Model(Prices{Home: 1.05, Draw: 20, Away: 60, Over25: 1.2, Under25: 4.5})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []float64{m.HomeCS, m.AwayCS} {
		if p < 0 || p > 1 {
			t.Errorf("clean-sheet prob out of range: %v", p)
		}
	}
}
