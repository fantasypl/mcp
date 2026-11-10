package algo

import (
	"context"
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Issue #54: chip_strategy's confidence was each chip's raw score, in units
// that differ by chip, so a Wildcard's 9.6 and a Triple Captain's 13 could
// not be compared, and the reasoning cited one or two factors.

// chipView is the part of a recommendation the tests read, whatever its type.
type chipView struct {
	ChipCode        string
	RecommendedGW   int
	ConfidenceScore float64
	*ChipConfidence
}

func chipRecs(t *testing.T, res *ChipStrategyResult) []chipView {
	t.Helper()
	var out []chipView
	for _, r := range res.Recommendations {
		switch v := r.(type) {
		case *BBoostRecommendation:
			out = append(out, chipView{v.ChipCode, v.RecommendedGameweek, v.ConfidenceScore, &v.ChipConfidence})
		case *TCRecommendation:
			out = append(out, chipView{v.ChipCode, v.RecommendedGameweek, v.ConfidenceScore, &v.ChipConfidence})
		case *FreeHitRecommendation:
			out = append(out, chipView{v.ChipCode, v.RecommendedGameweek, v.ConfidenceScore, &v.ChipConfidence})
		case *WildcardRecommendation:
			out = append(out, chipView{v.ChipCode, v.RecommendedGameweek, v.ConfidenceScore, &v.ChipConfidence})
		default:
			t.Fatalf("unexpected recommendation type %T", r)
		}
	}
	return out
}

func TestChipConfidenceIsComparableAcrossChips(t *testing.T) {
	e := newChipsEngine(t)
	got, err := e.ChipStrategy(context.Background(), 999001)
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*ChipStrategyResult)
	if res.ConfidenceScale == "" {
		t.Error("confidence_scale is not documented in the output")
	}
	recs := chipRecs(t, res)
	if len(recs) == 0 {
		t.Fatal("no recommendations")
	}
	for _, c := range recs {
		if c.NextBestGameweek == 0 || c.NextBestGameweek == c.RecommendedGW {
			t.Errorf("%s: next_best_gameweek = %d, want a different gameweek", c.ChipCode, c.NextBestGameweek)
		}
		if c.GapToNext != Round(c.ChipScore-c.NextBestScore, 1) {
			t.Errorf("%s: gap_to_next %.1f != chip_score %.1f - next_best_score %.1f", c.ChipCode, c.GapToNext, c.ChipScore, c.NextBestScore)
		}
		want := "low"
		switch {
		case c.GapToNext < 0:
			want = "low"
		case c.MarginPct >= 25:
			want = "high"
		case c.MarginPct >= 10:
			want = "medium"
		}
		if c.Confidence != want {
			t.Errorf("%s: confidence %q for a %.1f%% margin, want %q", c.ChipCode, c.Confidence, c.MarginPct, want)
		}
		if c.ConfidenceScore != c.ChipScore {
			t.Errorf("%s: deprecated confidence_score %.1f != chip_score %.1f", c.ChipCode, c.ConfidenceScore, c.ChipScore)
		}
	}
}

func TestChipCandidatesShowTheFactors(t *testing.T) {
	e := newChipsEngine(t)
	got, err := e.ChipStrategy(context.Background(), 999001)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chipRecs(t, got.(*ChipStrategyResult)) {
		if len(c.Candidates) < 2 || len(c.Candidates) > 5 {
			t.Fatalf("%s: %d candidates, want 2 to 5", c.ChipCode, len(c.Candidates))
		}
		for i := 1; i < len(c.Candidates); i++ {
			if c.Candidates[i].ChipScore > c.Candidates[i-1].ChipScore {
				t.Errorf("%s: candidates not best first: %+v", c.ChipCode, c.Candidates)
			}
		}
		for _, cand := range c.Candidates {
			if cand.Factors.FixtureCount == 0 {
				t.Errorf("%s GW%d: factors missing: %+v", c.ChipCode, cand.Gameweek, cand.Factors)
			}
		}
	}
	// The Bench Boost goes to the GW5 double; its candidate row says so.
	for _, c := range chipRecs(t, got.(*ChipStrategyResult)) {
		if c.ChipCode != "bboost" {
			continue
		}
		for _, cand := range c.Candidates {
			if cand.Gameweek == 5 && cand.Factors.DGWTeams == 0 {
				t.Errorf("GW5 candidate shows no double: %+v", cand.Factors)
			}
		}
	}
}

// First-half chips expire after GW19, so from GW11 the scan must stop at GW19
// rather than run to GW20.
func TestChipScanStopsAtTheHalfway(t *testing.T) {
	e := newChipsEngine(t)
	stub := e.client.(*StubClient)
	for i := range stub.bootstrap.Events {
		ev := &stub.bootstrap.Events[i]
		ev.IsCurrent, ev.IsNext, ev.Finished = ev.ID == 10, ev.ID == 11, ev.ID <= 10
	}
	stub.SetTeamPicks(999001, 10, loadJSON[*fpl.TeamPicks](t, testdataPath("chips_scenario", "picks.json")))
	got, err := e.ChipStrategy(context.Background(), 999001)
	if err != nil {
		t.Fatal(err)
	}
	res := got.(*ChipStrategyResult)
	if res.ScanWindow != "GW11-GW19" {
		t.Errorf("scan_window = %q, want GW11-GW19", res.ScanWindow)
	}
	for _, c := range chipRecs(t, res) {
		if c.RecommendedGW > 19 {
			t.Errorf("%s recommended for GW%d, after this half's chips expire", c.ChipCode, c.RecommendedGW)
		}
	}
}

// Confirmed doubles and blanks beyond the scan window are listed, so the
// season's big gameweeks are visible even before the scan reaches them.
func TestChipSeasonOutlookBeyondTheWindow(t *testing.T) {
	e := newChipsEngine(t)
	stub := e.client.(*StubClient)
	// Move one GW34 fixture into GW33: two clubs double in GW33 and blank in
	// GW34.
	for i := range stub.fixtures {
		if ev, ok := stub.fixtures[i].EventOf(); ok && ev == 34 {
			stub.fixtures[i].Event = ptr(33)
			break
		}
	}
	got, err := e.ChipStrategy(context.Background(), 999001)
	if err != nil {
		t.Fatal(err)
	}
	var gw33, gw34 *SeasonOutlookGW
	for i, g := range got.(*ChipStrategyResult).SeasonOutlook {
		switch g.Gameweek {
		case 33:
			gw33 = &got.(*ChipStrategyResult).SeasonOutlook[i]
		case 34:
			gw34 = &got.(*ChipStrategyResult).SeasonOutlook[i]
		}
	}
	if gw33 == nil || gw33.DGWTeams != 2 {
		t.Errorf("GW33 outlook = %+v, want 2 teams doubling", gw33)
	}
	if gw34 == nil || gw34.BlankTeams != 2 {
		t.Errorf("GW34 outlook = %+v, want 2 teams blanking", gw34)
	}
}

// With a league_id, rivals' remaining chips are listed.
func TestChipStrategyLeagueRivalChips(t *testing.T) {
	e := newChipsEngine(t)
	stub := e.client.(*StubClient)
	stub.SetLeague(777, &fpl.LeagueStandings{Standings: fpl.StandingsPage{Results: []fpl.LeagueEntry{
		{Entry: 999001, PlayerName: "Me", Rank: 1},
		{Entry: 999002, PlayerName: "Rival", Rank: 2},
	}}})
	stub.SetHistory(999002, &fpl.TeamHistory{Chips: []fpl.ChipUsage{{Name: "bboost", Event: 1}}})

	league := 777
	got, err := e.ChipStrategyWith(context.Background(), 999001, &league)
	if err != nil {
		t.Fatal(err)
	}
	rows := got.(*ChipStrategyResult).LeagueChips
	if len(rows) != 1 || rows[0].TeamID != 999002 {
		t.Fatalf("league_chips = %+v, want the one rival", rows)
	}
	if strings.Contains(strings.Join(rows[0].ChipsRemaining, ","), "Bench Boost") || len(rows[0].ChipsRemaining) != 3 {
		t.Errorf("rival chips_remaining = %v, want three chips without Bench Boost", rows[0].ChipsRemaining)
	}
}
