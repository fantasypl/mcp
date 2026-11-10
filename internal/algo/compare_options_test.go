package algo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
)

// newOptionsEngine serves the midseason bootstrap with gameweeks 1 to
// finishedThrough marked finished, plus synthetic per-gameweek histories
// for Haaland (411) and B.Fernandes (426).
func newOptionsEngine(t *testing.T, finishedThrough int, summaries map[int]*fpl.PlayerSummary) *Engine {
	t.Helper()
	b := loadJSON[*fpl.Bootstrap](t, testdataPath("bootstrap_midseason.json"))
	for i := range b.Events {
		b.Events[i].Finished = b.Events[i].ID <= finishedThrough
	}
	c := &StubClient{
		bootstrap:       b,
		fixtures:        loadJSON[[]fpl.Fixture](t, testdataPath("fixtures.json")),
		playerSummaries: summaries,
	}
	e := NewEngine(c)
	e.Now = func() time.Time { return goldenClock }
	return e
}

// match is one played match for a synthetic history.
func match(round, opponent, points int, home bool) fpl.PlayerHistoryEntry {
	return fpl.PlayerHistoryEntry{Round: round, OpponentTeam: opponent, TotalPoints: points, Minutes: 90, WasHome: home}
}

// The issue's motivating case: a steady 6-pointer and a one-haul player
// with the same total must not look the same.
func TestMeasureConsistencySeparatesSteadyFromHaul(t *testing.T) {
	steady := measureConsistency([]int{6, 6, 6, 6, 6, 6})
	haul := measureConsistency([]int{18, 3, 3, 3, 3, 6})

	if steady.TotalPoints != haul.TotalPoints || steady.MeanPoints != 6 || haul.MeanPoints != 6 {
		t.Fatalf("setup: both should total 36 at 6.0 a gameweek, got %+v and %+v", steady, haul)
	}
	if steady.Profile != "steady" || steady.StandardDeviation != 0 || steady.Hauls != 0 {
		t.Errorf("steady = %+v", steady)
	}
	if haul.Profile != "haul-dependent" || haul.Hauls != 1 || haul.HaulPoints != 18 || haul.HaulSharePct != 50 {
		t.Errorf("haul = %+v", haul)
	}
	if haul.MedianPoints != 3 || steady.MedianPoints != 6 {
		t.Errorf("medians = %v, %v; want 3 and 6", haul.MedianPoints, steady.MedianPoints)
	}
	if !strings.Contains(haul.Summary, "One haul of 10 or more (18 points) made 50% of the total") {
		t.Errorf("haul summary = %q", haul.Summary)
	}
}

func TestMeasureConsistencyProfiles(t *testing.T) {
	cases := []struct {
		points []int
		want   string
	}{
		{nil, "too few gameweeks"},
		{[]int{12, 2}, "too few gameweeks"},
		{[]int{1, 0, 2, 1}, "low output"},
		{[]int{2, 9, 1, 8, 3, 7}, "mixed"},           // mean 5, sd 3.1: no haul, but swingy
		{[]int{5, 6, 4, 7, 5, 3}, "steady"},          // mean 5, sd 1.3
		{[]int{0, 0, 15, 2, 1, 0}, "haul-dependent"}, // 15 of 18 from one week
	}
	for _, c := range cases {
		if got := measureConsistency(c.points); got.Profile != c.want {
			t.Errorf("measureConsistency(%v).Profile = %q, want %q (%+v)", c.points, got.Profile, c.want, got)
		}
	}
}

func TestBuildGameweekHistoryFoldsDoublesAndBlanks(t *testing.T) {
	teams := map[int]*fpl.Team{1: {ID: 1, ShortName: "ARS"}, 2: {ID: 2, ShortName: "AVL"}}
	s := &fpl.PlayerSummary{History: []fpl.PlayerHistoryEntry{
		match(3, 1, 9, true), // outside the window
		match(4, 1, 2, true),
		// Gameweek 5 is a double.
		{Round: 5, OpponentTeam: 2, TotalPoints: 8, Minutes: 90, GoalsScored: 1, ExpectedGoals: 0.555},
		{Round: 5, OpponentTeam: 1, TotalPoints: 3, Minutes: 60, WasHome: true, ExpectedGoals: 0.111},
		// Gameweek 6 is a blank: no entry.
	}}
	h := buildGameweekHistory(s, 4, 6, teams)

	if h.FromGameweek != 4 || h.ToGameweek != 6 || len(h.Gameweeks) != 3 {
		t.Fatalf("window = %d-%d with %d rows, want 4-6 with 3", h.FromGameweek, h.ToGameweek, len(h.Gameweeks))
	}
	dgw := h.Gameweeks[1]
	if dgw.Points != 11 || dgw.Minutes != 150 || dgw.Goals != 1 || len(dgw.Opponents) != 2 || dgw.Blank {
		t.Errorf("double gameweek row = %+v", dgw)
	}
	// 0.555 + 0.111 = 0.666, rounded once, not 0.56 + 0.11.
	if dgw.XG != 0.67 {
		t.Errorf("double gameweek xG = %v, want 0.67", dgw.XG)
	}
	if dgw.Opponents[0] != "AVL(A)" || dgw.Opponents[1] != "ARS(H)" {
		t.Errorf("opponents = %v", dgw.Opponents)
	}
	blank := h.Gameweeks[2]
	if !blank.Blank || blank.Points != 0 || len(blank.Opponents) != 0 {
		t.Errorf("blank row = %+v", blank)
	}
	if h.Consistency.Gameweeks != 3 || h.Consistency.TotalPoints != 13 {
		t.Errorf("consistency = %+v, want 3 gameweeks and 13 points", h.Consistency)
	}
}

func TestGameweekWindowSkipsUnfinished(t *testing.T) {
	b := &fpl.Bootstrap{Events: []fpl.Event{{ID: 1, Finished: true}, {ID: 2, Finished: true}, {ID: 3, IsCurrent: true}}}
	if from, to := gameweekWindow(b, 5); from != 1 || to != 2 {
		t.Errorf("gameweekWindow(5) = %d-%d, want 1-2", from, to)
	}
	if from, to := gameweekWindow(b, 1); from != 2 || to != 2 {
		t.Errorf("gameweekWindow(1) = %d-%d, want 2-2", from, to)
	}
	if _, to := gameweekWindow(&fpl.Bootstrap{Events: []fpl.Event{{ID: 1}}}, 5); to != 0 {
		t.Errorf("preseason window ends at %d, want 0", to)
	}
}

func optionsSummaries() map[int]*fpl.PlayerSummary {
	return map[int]*fpl.PlayerSummary{
		411: {History: []fpl.PlayerHistoryEntry{match(1, 1, 18, true), match(2, 2, 3, false), match(3, 3, 3, true), match(4, 4, 3, false), match(5, 5, 3, true), match(6, 6, 6, false)}},
		426: {History: []fpl.PlayerHistoryEntry{match(1, 1, 6, true), match(2, 2, 6, false), match(3, 3, 6, true), match(4, 4, 6, false), match(5, 5, 6, true), match(6, 6, 6, false)}},
	}
}

func TestComparePlayersWithOptionsFields(t *testing.T) {
	e := newOptionsEngine(t, 6, optionsSummaries())
	got, err := e.ComparePlayersWithOptions(context.Background(), []string{"Haaland", "B.Fernandes"}, 4,
		CompareOptions{Fields: []string{"cost_change_start", "now_cost", "now_cost", "photo"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.kind != compareKindSuccess {
		t.Fatalf("got %+v", got)
	}
	for _, p := range got.Players {
		if len(p.Fields) != 3 {
			t.Errorf("%s fields = %v, want the 3 distinct requested", p.Name, p.Fields)
		}
		var cost int
		if err := json.Unmarshal(p.Fields["now_cost"], &cost); err != nil || float64(cost)/10 != p.Cost {
			t.Errorf("%s now_cost = %s, want it to match cost %v", p.Name, p.Fields["now_cost"], p.Cost)
		}
		if _, ok := p.Fields["cost_change_start"]; !ok {
			t.Errorf("%s missing cost_change_start", p.Name)
		}
	}

	// The fields render inside each player and nothing else appears.
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"fields":{`) || strings.Contains(string(b), "gameweek_history") {
		t.Errorf("unexpected JSON shape: %s", b)
	}
}

func TestComparePlayersWithOptionsUnknownField(t *testing.T) {
	e := newOptionsEngine(t, 6, optionsSummaries())
	got, err := e.ComparePlayersWithOptions(context.Background(), []string{"Haaland", "B.Fernandes"}, 4,
		CompareOptions{Fields: []string{"now_cost", "nonsense", "also_bad"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.kind != compareKindError {
		t.Fatalf("want an error result, got %+v", got)
	}
	for _, want := range []string{"Unknown field(s): nonsense, also_bad.", "Valid fields: ", "cost_change_start", "now_cost"} {
		if !strings.Contains(got.Error, want) {
			t.Errorf("error %q lacks %q", got.Error, want)
		}
	}
}

func TestComparePlayersWithOptionsGameweeks(t *testing.T) {
	e := newOptionsEngine(t, 6, optionsSummaries())
	got, err := e.ComparePlayersWithOptions(context.Background(), []string{"Haaland", "B.Fernandes"}, 4,
		CompareOptions{IncludeGameweeks: 6})
	if err != nil {
		t.Fatal(err)
	}
	if got.GameweekHistoryNote != GameweekHistoryNote {
		t.Errorf("note = %q", got.GameweekHistoryNote)
	}
	profiles := map[string]string{}
	for _, p := range got.Players {
		if p.GameweekHistory == nil || len(p.GameweekHistory.Gameweeks) != 6 {
			t.Fatalf("%s gameweek_history = %+v", p.Name, p.GameweekHistory)
		}
		profiles[p.Name] = p.GameweekHistory.Consistency.Profile
	}
	if profiles["Haaland"] != "haul-dependent" || profiles["B.Fernandes"] != "steady" {
		t.Errorf("profiles = %v", profiles)
	}
}

func TestComparePlayersWithOptionsRejectsBadGameweeks(t *testing.T) {
	e := newOptionsEngine(t, 6, optionsSummaries())
	got, _ := e.ComparePlayersWithOptions(context.Background(), []string{"Haaland", "B.Fernandes"}, 4,
		CompareOptions{IncludeGameweeks: 39})
	if got.kind != compareKindError || !strings.Contains(got.Error, "between 1 and 38") {
		t.Errorf("got %+v", got)
	}
}

// With no options the output is byte-for-byte what it was before them.
func TestComparePlayersWithOptionsZeroValueUnchanged(t *testing.T) {
	e := newOptionsEngine(t, 6, optionsSummaries())
	ctx := context.Background()
	names := []string{"Haaland", "B.Fernandes"}
	want, err := e.ComparePlayersForTeam(ctx, names, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.ComparePlayersWithOptions(ctx, names, 4, CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wb, _ := json.Marshal(want)
	gb, _ := json.Marshal(got)
	if string(wb) != string(gb) {
		t.Errorf("zero options changed the output:\n got %s\nwant %s", gb, wb)
	}
}
