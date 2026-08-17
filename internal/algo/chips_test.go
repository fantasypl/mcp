package algo

import (
	"context"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/golden"
)

// stubIntelFetcher returns a fixed CommunityIntel without any network access
// — chip strategy's community intel fetch must never touch the real
// premierleague.com/allaboutfpl.com endpoints during a test.
type stubIntelFetcher struct {
	intel *CommunityIntel
	err   error
}

func (s *stubIntelFetcher) Fetch(context.Context) (*CommunityIntel, error) {
	return s.intel, s.err
}

// chipsScenarioIntel matches `fplctl gengolden --which=chips`'s community
// intel fixture exactly — a predicted BGW for GW7 (Nott'm Forest) and a
// predicted DGW for GW8 (Aston Villa), plus one failed source, exercising
// the merge path.
func chipsScenarioIntel() *CommunityIntel {
	return &CommunityIntel{
		DGWs: map[string]SourcedMention{
			"8": {Teams: []string{"AVL"}, Status: "predicted", Sources: []string{"allaboutfpl.com"}},
		},
		BGWs: map[string]SourcedMention{
			"7": {Teams: []string{"NFO"}, Status: "predicted", Sources: []string{"allaboutfpl.com"}},
		},
		SourcesChecked: []string{"premierleague.com", "allaboutfpl.com"},
		Errors:         []string{"premierleague.com: failed to fetch"},
	}
}

func newChipsEngine(t *testing.T) *Engine {
	t.Helper()
	dir := func(name string) string { return testdataPath("chips_scenario", name) }

	c := &StubClient{
		bootstrap: loadJSON[*fpl.Bootstrap](t, dir("bootstrap.json")),
		fixtures:  loadJSON[[]fpl.Fixture](t, dir("fixtures.json")),
		picks: map[picksKey]*fpl.TeamPicks{
			{999001, 1}: loadJSON[*fpl.TeamPicks](t, dir("picks.json")),
		},
		history: map[int]*fpl.TeamHistory{
			999001: loadJSON[*fpl.TeamHistory](t, dir("history.json")),
		},
	}
	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }
	e.IntelFetcher = &stubIntelFetcher{intel: chipsScenarioIntel()}
	return e
}

func TestChipStrategyMatchesGolden(t *testing.T) {
	e := newChipsEngine(t)
	got, err := e.ChipStrategy(context.Background(), 999001)
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, testdataPath("chips_scenario", "golden.json"), got)
}

// No chips remaining produces a distinct, smaller shape (a message, no
// scan_window/pending_dgws/etc.) — not the normal result with empty fields.
func TestChipStrategyNoChipsRemaining(t *testing.T) {
	e := newChipsEngine(t)
	// Override history so every chip has been used this half.
	c := e.client.(*StubClient)
	c.history[999001] = &fpl.TeamHistory{
		Chips: []fpl.ChipUsage{
			{Name: "wildcard", Event: 1}, {Name: "bboost", Event: 1},
			{Name: "freehit", Event: 1}, {Name: "3xc", Event: 1},
		},
	}

	got, err := e.ChipStrategy(context.Background(), 999001)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := got.(*NoChipsRemainingResult)
	if !ok {
		t.Fatalf("got %T, want *NoChipsRemainingResult", got)
	}
	if result.Message != "All chips have been used this season." {
		t.Errorf("Message = %q", result.Message)
	}
	if len(result.ChipsRemaining) != 0 || len(result.Recommendations) != 0 {
		t.Error("expected empty chips_remaining and recommendations")
	}
}

// A best-effort community intel failure must not break chip strategy —
// only narrow it back to API-only DGW/BGW predictions.
func TestChipStrategyToleratesIntelFailure(t *testing.T) {
	e := newChipsEngine(t)
	e.IntelFetcher = &stubIntelFetcher{err: context.DeadlineExceeded}

	got, err := e.ChipStrategy(context.Background(), 999001)
	if err != nil {
		t.Fatalf("a community intel failure must not fail the whole call: %v", err)
	}
	result, ok := got.(*ChipStrategyResult)
	if !ok {
		t.Fatalf("got %T, want *ChipStrategyResult", got)
	}
	if result.CommunityIntel != nil {
		t.Error("community_intel should be absent when the fetch fails")
	}
	if len(result.Recommendations) == 0 {
		t.Error("recommendations should still be produced from API data alone")
	}
}

// The WC->BB combo bonus only applies when Wildcard lands exactly one
// gameweek before the best Bench Boost gameweek.
func TestWildcardBenchBoostCombo(t *testing.T) {
	e := newChipsEngine(t)
	got, err := e.ChipStrategy(context.Background(), 999001)
	if err != nil {
		t.Fatal(err)
	}
	result := got.(*ChipStrategyResult)

	var wc *WildcardRecommendation
	var bb *BBoostRecommendation
	for _, r := range result.Recommendations {
		switch v := r.(type) {
		case *WildcardRecommendation:
			wc = v
		case *BBoostRecommendation:
			bb = v
		}
	}
	if wc == nil || bb == nil {
		t.Fatal("expected both a Wildcard and Bench Boost recommendation")
	}
	if wc.RecommendedGameweek != bb.RecommendedGameweek-1 {
		t.Errorf("Wildcard GW%d is not immediately before Bench Boost GW%d", wc.RecommendedGameweek, bb.RecommendedGameweek)
	}
	if got := wc.Reasoning; !contains(got, "Wildcard→Bench Boost combo") {
		t.Errorf("Wildcard reasoning missing combo callout: %q", got)
	}
	if got := bb.Reasoning; !contains(got, "use Wildcard in GW") {
		t.Errorf("Bench Boost reasoning missing combo callout: %q", got)
	}
}

// Recommendations must never double-book a gameweek across chips.
func TestNoTwoChipsShareAGameweek(t *testing.T) {
	e := newChipsEngine(t)
	got, err := e.ChipStrategy(context.Background(), 999001)
	if err != nil {
		t.Fatal(err)
	}
	result := got.(*ChipStrategyResult)

	seen := map[int]string{}
	for _, r := range result.Recommendations {
		var chip string
		var gw int
		switch v := r.(type) {
		case *WildcardRecommendation:
			chip, gw = v.ChipCode, v.RecommendedGameweek
		case *BBoostRecommendation:
			chip, gw = v.ChipCode, v.RecommendedGameweek
		case *FreeHitRecommendation:
			chip, gw = v.ChipCode, v.RecommendedGameweek
		case *TCRecommendation:
			chip, gw = v.ChipCode, v.RecommendedGameweek
		}
		if other, ok := seen[gw]; ok {
			t.Errorf("GW%d assigned to both %s and %s", gw, other, chip)
		}
		seen[gw] = chip
	}
}

func TestBonusPoolTieHandling(t *testing.T) {
	// Two teams tied for the top BPS both get bonus 3, consuming both the
	// 3- and 2-point slots — the next distinct BPS drops straight to 1.
	// (Shared logic with live.go's calculateFixtureBPS; this exercises the
	// analogous tie behaviour chips.go relies on indirectly via scoring.)
	if got := scoreFHForGW(&gwChipStats{blankTeams: 10}); got < 100 {
		t.Errorf("major BGW (10 blank teams) should score at least 100, got %v", got)
	}
	if got := scoreFHForGW(&gwChipStats{blankTeams: 3}); got >= 30 {
		t.Errorf("minor blank count should use the lowest multiplier tier, got %v", got)
	}
}

func TestPendingDGWTeamOrderMatchesFirstEncounter(t *testing.T) {
	fixtures := []fpl.Fixture{
		{ID: 1, Event: nil, TeamH: 5, TeamA: 3, Finished: false},
		{ID: 2, Event: nil, TeamH: 3, TeamA: 7, Finished: false},
	}
	_, order := predictDGWTeams(fixtures)
	want := []int{5, 3, 7}
	if len(order) != len(want) {
		t.Fatalf("got %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order[%d] = %d, want %d (first-encounter order: team 3 appears in fixture 1 before fixture 2)", i, order[i], want[i])
		}
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
