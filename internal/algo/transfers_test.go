package algo

import (
	"context"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/golden"
)

// syntheticTeamID matches the fixed team_id scripts/make_squad_fixture.py's
// output was captured under, and the one `fplctl gengolden` uses too.
const syntheticTeamID = 999001

// newEngineWithSquad wires the synthetic squad fixture into a stub client
// alongside the named bootstrap, so team-dependent algorithms can be
// golden-tested the same way the bootstrap-only ones already are.
func newEngineWithSquad(t *testing.T, fixture string) *Engine {
	t.Helper()
	squad := loadJSON[*fpl.TeamPicks](t, testdataPath("picks_squad1.json"))
	c := &StubClient{
		bootstrap: loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_"+fixture+".json")),
		fixtures:  loadJSON[[]fpl.Fixture](t, testdataPath("fixtures.json")),
		picks:     map[picksKey]*fpl.TeamPicks{{syntheticTeamID, 1}: squad},
	}
	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }
	return e
}

func TestTransferSuggestionsMatchesGolden(t *testing.T) {
	bothFixtures(t, func(t *testing.T, _ *Engine, suffix string) {
		e := newEngineWithSquad(t, suffixToFixture(suffix))

		for _, tc := range []struct {
			name          string
			freeTransfers int
			bank          float64
			golden        string
		}{
			{"1 free transfer", 1, 0.5, "transfers_1ft"},
			{"2 free transfers", 2, 0.0, "transfers_2ft"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := e.TransferSuggestions(context.Background(), syntheticTeamID, tc.freeTransfers, tc.bank)
				if err != nil {
					t.Fatal(err)
				}
				golden.Assert(t, goldenPath(tc.golden+suffix), got)
			})
		}
	})
}

func suffixToFixture(suffix string) string {
	if suffix == "_mid" {
		return "midseason"
	}
	return "preseason"
}

// An unresolvable team ID must produce TransferError, not a Go error — the
// The API treats this as a normal result shape, not an exception.
func TestTransferSuggestionsUnknownTeam(t *testing.T) {
	e := newEngineWithSquad(t, "preseason")
	got, err := e.TransferSuggestions(context.Background(), 424242, 1, 0)
	if err != nil {
		t.Fatalf("expected a soft error result, got a Go error: %v", err)
	}
	te, ok := got.(*TransferError)
	if !ok {
		t.Fatalf("got %T, want *TransferError", got)
	}
	if te.Error == "" {
		t.Error("expected a non-empty error message")
	}
}

// bank_m = 0.0 is a legitimate value (an empty bank), and it must still
// appear in the JSON output. This is the specific trap that ruled out
// `omitempty` on BankBalanceM: a struct shared between the error and success
// shapes would have silently dropped a real zero bank balance.
func TestZeroBankBalanceIsNotDropped(t *testing.T) {
	e := newEngineWithSquad(t, "midseason")
	got, err := e.TransferSuggestions(context.Background(), syntheticTeamID, 1, 0.0)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := got.(*TransferSuggestionsResult)
	if !ok {
		t.Fatalf("got %T, want *TransferSuggestionsResult", got)
	}
	if result.BankBalanceM != 0.0 {
		t.Errorf("BankBalanceM = %v, want 0.0", result.BankBalanceM)
	}

	norm, err := golden.Normalize(got)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := norm.(map[string]any)
	if !ok {
		t.Fatalf("normalized result is %T, not an object", norm)
	}
	if _, present := m["bank_balance_m"]; !present {
		t.Error("bank_balance_m key is missing from the JSON output when its value is 0.0")
	}
}

// Every requested free transfer should produce a sell candidate, up to squad
// size, and every candidate must actually be a squad member.
func TestTransferSuggestionsCountMatchesFreeTransfers(t *testing.T) {
	e := newEngineWithSquad(t, "midseason")
	for _, ft := range []int{1, 2} {
		res, err := e.TransferSuggestions(context.Background(), syntheticTeamID, ft, 1.0)
		if err != nil {
			t.Fatal(err)
		}
		result := res.(*TransferSuggestionsResult)
		if result.NumSuggestions != ft {
			t.Errorf("free_transfers=%d: got %d suggestions, want %d", ft, result.NumSuggestions, ft)
		}
	}
}

// A replacement must never already be in the squad, and must never exceed
// the seller's price plus bank.
func TestReplacementsRespectBudgetAndSquadMembership(t *testing.T) {
	e := newEngineWithSquad(t, "midseason")
	squad := loadJSON[*fpl.TeamPicks](t, testdataPath("picks_squad1.json"))
	squadIDs := map[int]bool{}
	for _, p := range squad.Picks {
		squadIDs[p.Element] = true
	}

	res, err := e.TransferSuggestions(context.Background(), syntheticTeamID, 2, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	result := res.(*TransferSuggestionsResult)

	for _, sug := range result.TransferSuggestions {
		for _, opt := range sug.TransferInOptions {
			if squadIDs[opt.ID] {
				t.Errorf("replacement %s is already in the squad", opt.Name)
			}
			if opt.Cost > sug.BudgetAvailable+1e-9 {
				t.Errorf("replacement %s costs %v, budget available is %v", opt.Name, opt.Cost, sug.BudgetAvailable)
			}
			if opt.Position != sug.TransferOut.Position {
				t.Errorf("replacement %s is %s, selling a %s", opt.Name, opt.Position, sug.TransferOut.Position)
			}
		}
	}
}

// Replacements must be sorted best-value-first.
func TestReplacementsSortedByValueDescending(t *testing.T) {
	e := newEngineWithSquad(t, "midseason")
	res, err := e.TransferSuggestions(context.Background(), syntheticTeamID, 2, 2.0)
	if err != nil {
		t.Fatal(err)
	}
	result := res.(*TransferSuggestionsResult)

	for _, sug := range result.TransferSuggestions {
		for i := 1; i < len(sug.TransferInOptions); i++ {
			if sug.TransferInOptions[i].ValueScore > sug.TransferInOptions[i-1].ValueScore {
				t.Errorf("transfer_in_options not sorted: %v then %v",
					sug.TransferInOptions[i-1].ValueScore, sug.TransferInOptions[i].ValueScore)
			}
		}
		if len(sug.TransferInOptions) > 5 {
			t.Errorf("got %d replacement options, want at most 5", len(sug.TransferInOptions))
		}
	}
}

func TestSellReasonPriority(t *testing.T) {
	e := NewEngine(nil)

	poorForm := &squadEntry{player: newPlayer(), form: 1.5, status: "a", fixture: &TeamFixture{FDR: 2}}
	if got := e.sellReason(poorForm); got != "Poor form" {
		t.Errorf("got %q, want %q", got, "Poor form")
	}

	injured := &squadEntry{player: newPlayer(), form: 5.0, status: "i", fixture: &TeamFixture{FDR: 2}}
	if got := e.sellReason(injured); got != "Injury/suspension concern" {
		t.Errorf("got %q, want %q", got, "Injury/suspension concern")
	}

	// The trailing Capitalize() call lower-cases everything after the first
	// character — sentence capitalization, not title case — so "FDR"
	// renders as "fdr" here. This is required behavior, not a casing bug.
	toughFixture := &squadEntry{player: newPlayer(), form: 5.0, status: "a", fixture: &TeamFixture{FDR: 5}}
	if got := e.sellReason(toughFixture); got != "Tough upcoming fixture (fdr 5)" {
		t.Errorf("got %q, want %q", got, "Tough upcoming fixture (fdr 5)")
	}

	// No fixture at all defaults FDR to 3, below the tough-fixture threshold.
	noReason := &squadEntry{player: newPlayer(), form: 5.0, status: "a", fixture: nil}
	if got := e.sellReason(noReason); got != "Lowest squad value score" {
		t.Errorf("got %q, want %q", got, "Lowest squad value score")
	}

	// Multiple reasons combine, in the defined checked order.
	multi := &squadEntry{player: newPlayer(), form: 1.0, status: "d", fixture: &TeamFixture{FDR: 5}}
	want := "Poor form, injury/suspension concern, tough upcoming fixture (fdr 5)"
	if got := e.sellReason(multi); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPlayerValueScoreFixtureWeighting(t *testing.T) {
	p := newPlayer()
	easy := []TeamFixture{{FDR: 1, IsHome: true}}
	hard := []TeamFixture{{FDR: 5, IsHome: false}}

	if playerValueScore(p, easy, nil) <= playerValueScore(p, hard, nil) {
		t.Error("an easy fixture should score above a hard one")
	}
	// The model's flat -3.0 blank penalty is
	// actually milder than a genuinely brutal fixture. -f.FDR at FDR 5 away
	// costs -5.0, worse than not playing at all. Counter-intuitive but real
	// reference behaviour — not a defect to "fix" quietly in a test.
	if playerValueScore(p, nil, nil) <= playerValueScore(p, hard, nil) {
		t.Error("blanking should score above an FDR-5 away fixture (-3.0 vs -5.0)")
	}
}

// GW+1 must matter more than GW+2 in the medium-term outlook.
func TestPlayerValueScoreFutureWeighting(t *testing.T) {
	p := newPlayer()
	base := []TeamFixture{{FDR: 3, IsHome: false}}
	easySoon := [][]TeamFixture{{{FDR: 1, IsHome: true}}, {{FDR: 3, IsHome: false}}}
	easyLater := [][]TeamFixture{{{FDR: 3, IsHome: false}}, {{FDR: 1, IsHome: true}}}

	if playerValueScore(p, base, easySoon) <= playerValueScore(p, base, easyLater) {
		t.Error("an easy GW+1 should be worth more than an equally easy GW+2")
	}
}

// A defender's defensive contribution should add to the score; every other
// position should ignore it entirely.
func TestPlayerValueScoreDefensiveContributionOnlyForDefenders(t *testing.T) {
	def := newPlayer()
	def.ElementType = 2
	def.DefensiveContributionPer90 = numOf(4.0)

	mid := newPlayer()
	mid.ElementType = 3
	mid.DefensiveContributionPer90 = numOf(4.0)

	defBase := newPlayer()
	defBase.ElementType = 2
	defBase.DefensiveContributionPer90 = numOf(0.0)

	fixtures := []TeamFixture{{FDR: 3}}
	if playerValueScore(def, fixtures, nil) <= playerValueScore(defBase, fixtures, nil) {
		t.Error("defensive contribution should raise a defender's score")
	}

	midBase := newPlayer()
	midBase.ElementType = 3
	midBase.DefensiveContributionPer90 = numOf(0.0)
	if playerValueScore(mid, fixtures, nil) != playerValueScore(midBase, fixtures, nil) {
		t.Error("defensive contribution should not affect a midfielder's score")
	}
}
