package algo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// pedroSquad mirrors the ids and ownership from the real data behind issue #2:
// Pedro Porro is the only web_name that *starts with* "Pedro", but João Pedro
// (web_name "João Pedro") is owned far more widely.
func pedroSquad() []fpl.Player {
	return []fpl.Player{
		{ID: 499, WebName: "Pedro Porro", FirstName: "Pedro", SecondName: "Porro Sauceda", SelectedByPercent: 24.0, TotalPoints: 117},
		{ID: 165, WebName: "João Pedro", FirstName: "João Pedro", SecondName: "Junqueira de Jesus", SelectedByPercent: 53.7, TotalPoints: 177},
		{ID: 156, WebName: "Neto", FirstName: "Pedro", SecondName: "Lomba Neto", SelectedByPercent: 1.2, TotalPoints: 125},
	}
}

// Issue #2, acceptance 1: the ASCII spelling and the accented spelling are the
// same query.
func TestFuzzyMatchIgnoresDiacritics(t *testing.T) {
	for _, q := range []string{"Joao Pedro", "João Pedro", "joao pedro", "JOÃO PEDRO"} {
		m, ok := fuzzyMatchPlayer(q, pedroSquad())
		if !ok {
			t.Fatalf("%q: no match", q)
		}
		if m.player.ID != 165 || m.tier != "exact" {
			t.Errorf("%q: got player %d tier %q, want 165 exact", q, m.player.ID, m.tier)
		}
	}
}

// Issue #2, acceptance 2: a short ambiguous query is resolved by ownership
// across match tiers, not by which tier happens to catch a name first, and
// the losing candidates are reported.
func TestFuzzyMatchAmbiguousQueryPrefersOwnership(t *testing.T) {
	m, ok := fuzzyMatchPlayer("Pedro", pedroSquad())
	if !ok {
		t.Fatal("no match")
	}
	if m.player.ID != 165 {
		t.Errorf("matched player %d (%s), want 165 (João Pedro, 53.7%% owned)", m.player.ID, m.player.WebName)
	}
	// Pedro is a whole word of "João Pedro", so it counts as a prefix match.
	if m.tier != "starts_with" {
		t.Errorf("tier = %q, want starts_with", m.tier)
	}
	if len(m.alternatives) != 1 || m.alternatives[0].ID != 499 {
		t.Errorf("alternatives = %v, want just Pedro Porro (499)", m.alternatives)
	}
}

// A real prefix outranks a mere substring, whatever the ownership: "Son" means
// a player whose name starts with Son, not the most-owned Robertson or Jackson.
func TestFuzzyMatchPrefixBeatsMoreOwnedSubstring(t *testing.T) {
	players := []fpl.Player{
		{ID: 1, WebName: "Robertson", FirstName: "Andrew", SecondName: "Robertson", SelectedByPercent: 40.0},
		{ID: 2, WebName: "Jackson", FirstName: "Nicolas", SecondName: "Jackson", SelectedByPercent: 30.0},
		{ID: 3, WebName: "Sonny", FirstName: "Sonny", SecondName: "Nobody", SelectedByPercent: 2.0},
	}
	m, ok := fuzzyMatchPlayer("Son", players)
	if !ok {
		t.Fatal("no match")
	}
	if m.player.ID != 3 || m.tier != "starts_with" {
		t.Errorf("got %d %q, want Sonny (3) via starts_with", m.player.ID, m.tier)
	}
	if len(m.alternatives) != 0 {
		t.Errorf("alternatives = %v, want none: the substring matches are a lower tier", m.alternatives)
	}
}

// With no prefix match at all, a substring match still resolves, most-owned first.
func TestFuzzyMatchFallsBackToSubstring(t *testing.T) {
	players := []fpl.Player{
		{ID: 2, WebName: "Jackson", FirstName: "Nicolas", SecondName: "Jackson", SelectedByPercent: 30.0},
		{ID: 1, WebName: "Robertson", FirstName: "Andrew", SecondName: "Robertson", SelectedByPercent: 40.0},
	}
	m, _ := fuzzyMatchPlayer("son", players)
	if m.player.ID != 1 || m.tier != "contains" {
		t.Errorf("got %d %q, want Robertson (1) via contains", m.player.ID, m.tier)
	}
}

// An exact web_name is never overridden by a more-owned partial match.
func TestFuzzyMatchExactBeatsOwnership(t *testing.T) {
	players := append(pedroSquad(), fpl.Player{ID: 1, WebName: "Pedro", FirstName: "Pedro", SecondName: "Nobody", SelectedByPercent: 0.1})
	m, _ := fuzzyMatchPlayer("Pedro", players)
	if m.player.ID != 1 || m.tier != "exact" {
		t.Errorf("got %d %q, want the exact web_name Pedro (1)", m.player.ID, m.tier)
	}
}

// Issue #2, acceptance 3: matchWarnings turns non-exact or ambiguous matches
// into top-level warnings, so a caller who never looks at match_confidence
// still sees them.
func TestMatchWarnings(t *testing.T) {
	players := pedroSquad()
	m, _ := fuzzyMatchPlayer("Pedro", players)
	warnings := matchWarnings([]queryMatch{{query: "Pedro", playerMatch: m}}, map[int]string{165: "CHE", 499: "TOT"})
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	w := warnings[0]
	for _, want := range []string{"Pedro", "João Pedro", "CHE", "Pedro Porro", "TOT"} {
		if !containsFold(w, want) {
			t.Errorf("warning %q is missing %q", w, want)
		}
	}

	exact, _ := fuzzyMatchPlayer("Joao Pedro", players)
	if got := matchWarnings([]queryMatch{{query: "Joao Pedro", playerMatch: exact}}, nil); len(got) != 0 {
		t.Errorf("an unambiguous exact match should not warn, got %v", got)
	}
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// End to end: the warning reaches the serialized result, ahead of the players,
// and an all-exact comparison carries none.
func TestComparePlayersWarningInOutput(t *testing.T) {
	e := newCompareEngine(t, "preseason")

	got, err := e.ComparePlayers(context.Background(), []string{"Haaland", "B.Fern"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Warnings) != 1 || !strings.Contains(decoded.Warnings[0], "B.Fern") {
		t.Errorf("warnings = %v, want one naming the 'B.Fern' query", decoded.Warnings)
	}
	if !strings.HasPrefix(string(out), `{"warnings"`) {
		t.Errorf("warnings should be the first key, got %.40s", out)
	}

	exact, err := e.ComparePlayers(context.Background(), []string{"Haaland", "B.Fernandes"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if exact.Warnings != nil {
		t.Errorf("all-exact comparison should carry no warnings, got %v", exact.Warnings)
	}
}
