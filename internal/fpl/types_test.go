package fpl

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

func TestNumUnmarshal(t *testing.T) {
	cases := []struct {
		in      string
		want    float64
		wantErr bool
	}{
		// FPL sends form, ep_next, points_per_game and friends as strings.
		{`"6.2"`, 6.2, false},
		{`"0.0"`, 0, false},
		{`"-1.5"`, -1.5, false},
		{`"381.4"`, 381.4, false},

		// Per-90 stats arrive as bare numbers.
		{`0.777`, 0.777, false},
		{`0`, 0, false},
		{`42`, 42, false},

		// Absent means zero for these quantities.
		{`null`, 0, false},
		{`""`, 0, false},
		// vaastav's CSV export writes the literal string "None".
		{`"None"`, 0, false},

		{`"abc"`, 0, true},
		{`{}`, 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			var n Num
			err := json.Unmarshal([]byte(tc.in), &n)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %s, got %v", tc.in, n)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if math.Abs(n.Float()-tc.want) > 1e-9 {
				t.Errorf("got %v, want %v", n.Float(), tc.want)
			}
		})
	}
}

// Num must survive being embedded in a struct, which is how it is actually used.
func TestNumInStruct(t *testing.T) {
	var p Player
	raw := `{"id":411,"form":"5.4","expected_goals_per_90":0.777,"ep_this":null}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if p.Form.Float() != 5.4 || p.EPThis.Float() != 0 {
		t.Errorf("form=%v ep_this=%v", p.Form, p.EPThis)
	}
}

func loadBootstrap(t *testing.T, path string) *Bootstrap {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bs Bootstrap
	if err := json.Unmarshal(b, &bs); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return &bs
}

// Decoding the real payload is the test that matters: a hand-written struct
// that silently fails to match production JSON is a classic compatibility bug.
func TestDecodeRealBootstrap(t *testing.T) {
	bs := loadBootstrap(t, "../../testdata/bootstrap_preseason.json")

	if got := len(bs.Elements); got != 564 {
		t.Errorf("elements = %d, want 564", got)
	}
	if got := len(bs.Teams); got != 20 {
		t.Errorf("teams = %d, want 20", got)
	}
	if got := len(bs.Events); got != 38 {
		t.Errorf("events = %d, want 38", got)
	}
	if got := len(bs.ElementTypes); got != 4 {
		t.Errorf("element types = %d, want 4", got)
	}

	// Haaland, id 411. Values cross-checked against the golden output.
	var h *Player
	for i := range bs.Elements {
		if bs.Elements[i].ID == 411 {
			h = &bs.Elements[i]
			break
		}
	}
	if h == nil {
		t.Fatal("player 411 not found")
	}
	if h.WebName != "Haaland" {
		t.Errorf("web_name = %q", h.WebName)
	}
	if h.TotalPoints != 239 {
		t.Errorf("total_points = %d, want 239", h.TotalPoints)
	}
	if h.PointsPerGame.Float() != 6.8 {
		t.Errorf("points_per_game = %v, want 6.8", h.PointsPerGame)
	}
	if h.Minutes != 2953 {
		t.Errorf("minutes = %d, want 2953", h.Minutes)
	}
	if math.Abs(h.ExpectedGoalsConcededPer90.Float()) < 0 {
		t.Error("unreachable")
	}
	// Preseason: form resets to "0.0" for everyone.
	if h.Form.Float() != 0 {
		t.Errorf("preseason form = %v, want 0", h.Form)
	}
	// On penalties, so the order is 1 rather than absent.
	if h.PenaltiesOrder == nil || *h.PenaltiesOrder != 1 {
		t.Errorf("penalties_order = %v, want 1", h.PenaltiesOrder)
	}
}

// The null-vs-zero distinction the plan flagged as the highest-risk trap:
// null chance_of_playing means fit, 0 means definitely out.
func TestNullableSemantics(t *testing.T) {
	for _, tc := range []struct {
		path              string
		wantNull, wantSet int
	}{
		{"../../testdata/bootstrap_preseason.json", 507, 57},
		{"../../testdata/bootstrap_midseason.json", 181, 383},
	} {
		bs := loadBootstrap(t, tc.path)
		var null, set int
		for _, p := range bs.Elements {
			if p.ChanceOfPlayingNextRound == nil {
				null++
			} else {
				set++
			}
		}
		if null != tc.wantNull || set != tc.wantSet {
			t.Errorf("%s: null=%d set=%d, want null=%d set=%d",
				tc.path, null, set, tc.wantNull, tc.wantSet)
		}
	}
}

// The midseason fixture exists specifically to exercise the form term, which
// the live preseason payload leaves dead. Guard that property.
func TestMidseasonFixtureHasForm(t *testing.T) {
	bs := loadBootstrap(t, "../../testdata/bootstrap_midseason.json")
	var withForm int
	for _, p := range bs.Elements {
		if p.Form.Float() > 0 {
			withForm++
		}
	}
	if withForm != 318 {
		t.Errorf("players with nonzero form = %d, want 318", withForm)
	}

	pre := loadBootstrap(t, "../../testdata/bootstrap_preseason.json")
	for _, p := range pre.Elements {
		if p.Form.Float() != 0 {
			t.Fatalf("preseason fixture unexpectedly has form for %s", p.WebName)
			break
		}
	}
}

func TestGameweekResolution(t *testing.T) {
	bs := loadBootstrap(t, "../../testdata/bootstrap_preseason.json")
	// Preseason: nothing is current, GW1 is next.
	if got := bs.NextGameweek(); got != 1 {
		t.Errorf("NextGameweek = %d, want 1", got)
	}
	// No current gameweek, so CurrentGameweek falls through to is_next.
	if got := bs.CurrentGameweek(); got != 1 {
		t.Errorf("CurrentGameweek = %d, want 1", got)
	}
}

func TestGameweekFallbacks(t *testing.T) {
	cases := []struct {
		name        string
		events      []Event
		wantCurrent int
		wantNext    int
	}{
		{"current wins", []Event{{ID: 5, IsCurrent: true}, {ID: 6, IsNext: true}}, 5, 6},
		{"next when no current", []Event{{ID: 6, IsNext: true}}, 6, 6},
		{"last finished when neither", []Event{{ID: 1, Finished: true}, {ID: 2, Finished: true}}, 2, 2},
		{"one when empty", nil, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bootstrap{Events: tc.events}
			if got := b.CurrentGameweek(); got != tc.wantCurrent {
				t.Errorf("CurrentGameweek = %d, want %d", got, tc.wantCurrent)
			}
			if got := b.NextGameweek(); got != tc.wantNext {
				t.Errorf("NextGameweek = %d, want %d", got, tc.wantNext)
			}
		})
	}
}

func TestDecodeFixtures(t *testing.T) {
	b, err := os.ReadFile("../../testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fs []Fixture
	if err := json.Unmarshal(b, &fs); err != nil {
		t.Fatal(err)
	}
	if len(fs) != 380 {
		t.Errorf("fixtures = %d, want 380", len(fs))
	}

	var gw1 int
	for _, f := range fs {
		if f.InGameweek(1) {
			gw1++
		}
	}
	if gw1 != 10 {
		t.Errorf("GW1 fixtures = %d, want 10", gw1)
	}

	// Scores are null before kickoff.
	for _, f := range fs {
		if !f.Finished && f.TeamHScore != nil {
			t.Errorf("fixture %d unplayed but has a score", f.ID)
			break
		}
	}

	// An unassigned fixture must not match any gameweek.
	var unassigned Fixture
	if err := json.Unmarshal([]byte(`{"id":1,"event":null}`), &unassigned); err != nil {
		t.Fatal(err)
	}
	if _, ok := unassigned.EventOf(); ok {
		t.Error("null event marked as assigned")
	}
	for gw := 1; gw <= 38; gw++ {
		if unassigned.InGameweek(gw) {
			t.Errorf("null-event fixture matched GW%d", gw)
			break
		}
	}
}

// Every modelled field must actually be present in the real payload. A typo in
// a json tag decodes to the zero value in silence, which is precisely the class
// of bug golden files would then enshrine.
func TestNoSilentlyUnmappedFields(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/bootstrap_preseason.json")
	if err != nil {
		t.Fatal(err)
	}
	var loose struct {
		Elements []map[string]any `json:"elements"`
		Teams    []map[string]any `json:"teams"`
		Events   []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(raw, &loose); err != nil {
		t.Fatal(err)
	}

	unmapped := func(typ any, sample map[string]any) []string {
		b, _ := json.Marshal(typ)
		var mapped map[string]any
		if err := json.Unmarshal(b, &mapped); err != nil {
			t.Fatal(err)
		}
		var bad []string
		for k := range mapped {
			if _, ok := sample[k]; !ok {
				bad = append(bad, k)
			}
		}
		return bad
	}

	for _, tc := range []struct {
		name   string
		typ    any
		sample map[string]any
	}{
		{"Player", Player{}, loose.Elements[0]},
		{"Team", Team{}, loose.Teams[0]},
		{"Event", Event{}, loose.Events[0]},
	} {
		if bad := unmapped(tc.typ, tc.sample); len(bad) > 0 {
			t.Errorf("%s: json tags absent from the real payload: %v", tc.name, bad)
		}
	}

	// Prove the check above can fail. A tag typo decodes to the zero value in
	// silence, so a vacuous guard here would be worse than none at all.
	type typoed struct {
		Form Num `json:"fomr"`
	}
	if bad := unmapped(typoed{}, loose.Elements[0]); len(bad) != 1 || bad[0] != "fomr" {
		t.Errorf("guard is vacuous: expected it to flag \"fomr\", got %v", bad)
	}
}
