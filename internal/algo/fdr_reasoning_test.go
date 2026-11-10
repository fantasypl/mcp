package algo

import (
	"strings"
	"testing"
)

// Issue #46: reasoning printed FDR truncated to an integer, so a 1.8 fixture
// read "fdr 1", the same as a 1.4. Each case is from the issue. Reasoning is
// capitalised like Python's str.capitalize, so compare in lower case.
func TestCaptainReasoningShowsFDRToOneDecimal(t *testing.T) {
	e := NewEngine(nil)
	cases := []struct {
		name string
		fdr  float64
		home bool
		want string
	}{
		{"Haaland v NFO (A)", 1.4, false, "easy fixture (fdr 1.4)"},
		{"Gross v BRE (H)", 1.8, true, "easy fixture (fdr 1.8)"},
		{"Konsa v HUL (H)", 1.4, true, "easy fixture (fdr 1.4)"},
		{"a tough one", 4.6, false, "tough fixture (fdr 4.6)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.ToLower(e.buildReasoning(newPlayer(), []TeamFixture{{FDR: tc.fdr, IsHome: tc.home}}, 10))
			if !strings.Contains(got, tc.want) {
				t.Errorf("reasoning %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// transfer_suggestions' sell reason had the same truncation.
func TestSellReasonShowsFDRToOneDecimal(t *testing.T) {
	e := NewEngine(nil)
	se := &squadEntry{form: 5, status: "a", fixture: &TeamFixture{FDR: 4.6}}
	if got := strings.ToLower(e.sellReason(se)); !strings.Contains(got, "fdr 4.6") {
		t.Errorf("sell reason %q, want fdr 4.6", got)
	}
}
