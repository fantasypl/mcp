package algo

import (
	"context"
	"testing"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
	"github.com/ajitem/fpl-intelligence/internal/golden"
)

// newLiveEngine wires the hand-built live scenario `fplctl gengolden
// --which=live` produces into a stub client. It covers BPS ties, confirmed
// vs projected bonus, and a bench too small to cover every unused starter.
func newLiveEngine(t *testing.T) *Engine {
	t.Helper()
	c := &stubClient{
		bootstrap:   loadJSON[*fpl.Bootstrap](t, testdataPath("live_scenario", "bootstrap.json")),
		fixtures:    loadJSON[[]fpl.Fixture](t, testdataPath("live_scenario", "fixtures.json")),
		picks:       map[picksKey]*fpl.TeamPicks{{syntheticTeamID, 1}: loadJSON[*fpl.TeamPicks](t, testdataPath("live_scenario", "picks.json"))},
		live:        map[int]*fpl.LiveResponse{1: loadJSON[*fpl.LiveResponse](t, testdataPath("live_scenario", "live.json"))},
		eventStatus: loadJSON[*fpl.EventStatusResponse](t, testdataPath("live_scenario", "event_status.json")),
	}
	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }
	return e
}

func TestLivePointsMatchesGolden(t *testing.T) {
	e := newLiveEngine(t)
	got, err := e.LivePoints(context.Background(), syntheticTeamID)
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, testdataPath("live_scenario", "golden.json"), got)
}

// The scenario's headline case: three starters read 0 minutes but only two
// bench players are eligible (one bench player also has 0 minutes), so the
// third — A.Garcia — must get no suggested substitute rather than reusing an
// already-assigned bench player or inventing one.
func TestAutoSubExhaustsEligibleBench(t *testing.T) {
	e := newLiveEngine(t)
	got, err := e.LivePoints(context.Background(), syntheticTeamID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AutoSubScenarios) != 3 {
		t.Fatalf("got %d auto-sub scenarios, want 3", len(got.AutoSubScenarios))
	}
	for _, s := range got.AutoSubScenarios {
		if s.Out == "A.García" || s.In == "A.García" {
			t.Error("A.Garcia should have no eligible substitute and not appear in either role")
		}
	}
	seen := map[string]bool{}
	for _, s := range got.AutoSubScenarios {
		if seen[s.In] {
			t.Errorf("bench player %s used as a substitute more than once", s.In)
		}
		seen[s.In] = true
	}
}

// GKP starters can only be replaced by the bench GKP, never an outfield
// bench player, even though outfield bench players are processed in the same
// pass.
func TestGKPAutoSubOnlyUsesGKPBench(t *testing.T) {
	e := newLiveEngine(t)
	got, err := e.LivePoints(context.Background(), syntheticTeamID)
	if err != nil {
		t.Fatal(err)
	}
	var gkpSub *AutoSub
	for i, s := range got.AutoSubScenarios {
		if s.Out == "Pickford" {
			gkpSub = &got.AutoSubScenarios[i]
		}
	}
	if gkpSub == nil {
		t.Fatal("expected a GKP auto-sub for Pickford")
	}
	if gkpSub.In != "Forster" {
		t.Errorf("Pickford subbed for %s, want the bench GKP Forster", gkpSub.In)
	}
}

// Two players tied at the top BPS both receive the higher bonus (3), which
// then skips the 2-point slot entirely for the next distinct BPS value.
func TestBPSTiesShareBonusAndSkipSlot(t *testing.T) {
	e := newLiveEngine(t)
	got, err := e.LivePoints(context.Background(), syntheticTeamID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]LivePlayer{}
	for _, s := range got.Starters {
		byName[s.Name] = s
	}

	gabriel, rice, timber := byName["Gabriel"], byName["Rice"], byName["J.Timber"]
	if gabriel.BonusProjection.ProjectedBonus != 0 || gabriel.ConfirmedBonus != 3 {
		t.Errorf("Gabriel: confirmed=%d projected=%d, want confirmed=3", gabriel.ConfirmedBonus, gabriel.BonusProjection.ProjectedBonus)
	}
	if rice.ConfirmedBonus != 3 {
		t.Errorf("Rice: confirmed=%d, want 3 (tied with Gabriel)", rice.ConfirmedBonus)
	}
	if gabriel.BonusProjection.BPSRank == nil || *gabriel.BonusProjection.BPSRank != 1 {
		t.Errorf("Gabriel bps_rank = %v, want 1", gabriel.BonusProjection.BPSRank)
	}
	if rice.BonusProjection.BPSRank == nil || *rice.BonusProjection.BPSRank != 1 {
		t.Errorf("Rice bps_rank = %v, want 1 (tied)", rice.BonusProjection.BPSRank)
	}
	// The tie at rank 1 consumes both the 3- and 2-point slots, so the next
	// distinct BPS value skips straight to rank 3 and the 1-point bonus.
	if timber.BonusProjection.BPSRank == nil || *timber.BonusProjection.BPSRank != 3 {
		t.Errorf("J.Timber bps_rank = %v, want 3 (rank skips past the consumed tie slots)", timber.BonusProjection.BPSRank)
	}
	if timber.ConfirmedBonus != 1 {
		t.Errorf("J.Timber confirmed bonus = %d, want 1", timber.ConfirmedBonus)
	}
}

// A confirmed bonus (stats.bonus > 0) takes priority over the BPS
// projection, and the narrative reports the confirmed figure rather than a
// projection sentence.
func TestConfirmedBonusOverridesProjection(t *testing.T) {
	e := newLiveEngine(t)
	got, err := e.LivePoints(context.Background(), syntheticTeamID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got.Starters {
		if s.Name != "Gabriel" {
			continue
		}
		if s.BonusStatus != "confirmed" {
			t.Errorf("bonus_status = %q, want confirmed", s.BonusStatus)
		}
		if s.BonusProjection.Narrative != "Bonus confirmed: 3 pts" {
			t.Errorf("narrative = %q", s.BonusProjection.Narrative)
		}
	}
}

// A fixture that hasn't kicked off yet must report status "not_started" with
// an empty top_bps list, not an error or a zero-BPS ranking.
func TestNotStartedFixtureReportsEmptyBPS(t *testing.T) {
	e := newLiveEngine(t)
	got, err := e.LivePoints(context.Background(), syntheticTeamID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range got.MatchBPS {
		if m.Match == "MCI vs LIV" {
			found = true
			if m.Status != "not_started" {
				t.Errorf("status = %q, want not_started", m.Status)
			}
			if len(m.TopBPS) != 0 {
				t.Errorf("top_bps = %v, want empty", m.TopBPS)
			}
		}
	}
	if !found {
		t.Fatal("MCI vs LIV fixture not found in match_bps")
	}
}

// bonus_status is "provisional" unless every recorded day has confirmed
// bonus — the fixture's event_status has one confirmed day and one not.
func TestOverallBonusStatusRequiresEveryDayConfirmed(t *testing.T) {
	e := newLiveEngine(t)
	got, err := e.LivePoints(context.Background(), syntheticTeamID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BonusStatus != "provisional" {
		t.Errorf("bonus_status = %q, want provisional", got.BonusStatus)
	}
}

// live_total sums only the starting XI's contributed points — bench points
// never count even when a bench player has a strong live score, since no
// auto-sub is automatically "applied" to the total shown here.
func TestLiveTotalExcludesBench(t *testing.T) {
	e := newLiveEngine(t)
	got, err := e.LivePoints(context.Background(), syntheticTeamID)
	if err != nil {
		t.Fatal(err)
	}
	sum := 0
	for _, s := range got.Starters {
		sum += s.ContributedPoints
	}
	if got.LiveTotal != sum {
		t.Errorf("live_total = %d, want %d (sum of starters only)", got.LiveTotal, sum)
	}
}
