package algo

import (
	"context"
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// captainPlayers builds the bootstrap players captainChoice reads attacking
// threat from: minutes and expected goal involvements.
func captainPlayers(ps ...fpl.Player) map[int]*fpl.Player {
	m := make(map[int]*fpl.Player, len(ps))
	for i := range ps {
		m[ps[i].ID] = &ps[i]
	}
	return m
}

const (
	sakaID = iota + 1
	bogleID
	haalandID
	gibbsWhiteID
)

// The players behind issue #23's report. Saka is an attacking midfielder;
// Bogle is a £4.6m defender whose ep_next comes from clean sheets and
// defensive contributions, with few attacking returns.
var (
	pSaka       = fpl.Player{ID: sakaID, WebName: "Saka", ElementType: 3, NowCost: 104, Minutes: 900, ExpectedGoalInvolvements: 6.5}
	pBogle      = fpl.Player{ID: bogleID, WebName: "Bogle", ElementType: 2, NowCost: 46, Minutes: 900, ExpectedGoalInvolvements: 1.0}
	pHaaland    = fpl.Player{ID: haalandID, WebName: "Haaland", ElementType: 4, NowCost: 145, Minutes: 900, ExpectedGoalInvolvements: 9.0}
	pGibbsWhite = fpl.Player{ID: gibbsWhiteID, WebName: "Gibbs-White", ElementType: 3, NowCost: 75, Minutes: 900, ExpectedGoalInvolvements: 3.5}
	allFour     = captainPlayers(pSaka, pBogle, pHaaland, pGibbsWhite)
	asStarters  = func(entries ...HubSquadEntry) []HubSquadEntry {
		for i := range entries {
			entries[i].Starter = true
			entries[i].Slot = i + 1
		}
		return entries
	}
)

// Issue #23 regression: ep_next ranked Bogle, a £4.6m defender, above Saka.
// The hub must still recommend Saka, and say why in one line.
func TestCaptainChoiceSakaOverBogle(t *testing.T) {
	squad := asStarters(
		HubSquadEntry{ElementID: sakaID, Name: "Saka", Position: "MID", Cost: 10.4, CaptainScore: 12.4, EPNext: 6.1},
		HubSquadEntry{ElementID: bogleID, Name: "Bogle", Position: "DEF", Cost: 4.6, CaptainScore: 8.9, EPNext: 6.8},
		HubSquadEntry{ElementID: gibbsWhiteID, Name: "Gibbs-White", Position: "MID", Cost: 7.5, CaptainScore: 10.2, EPNext: 5.0},
	)
	got := captainChoice(squad, allFour)
	if got == nil || got.Name != "Saka" {
		t.Fatalf("captainChoice = %+v, want Saka", got)
	}
	for _, want := range []string{"Saka", "Bogle", "6.8", "attacking threat", "xGI/90 0.10"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("reason %q is missing %q", got.Reason, want)
		}
	}
	if strings.Contains(got.Reason, "\n") {
		t.Errorf("reason %q spans more than one line", got.Reason)
	}
}

// The sanity check matters most inside the tie band: Bogle's captain_score
// is within 5% of Saka's and his ep_next is higher, but ep_next can't break
// the tie for a player with no attacking threat.
func TestCaptainChoiceSanityCheckInsideTieBand(t *testing.T) {
	squad := asStarters(
		HubSquadEntry{ElementID: sakaID, Name: "Saka", CaptainScore: 12.4, EPNext: 6.1},
		HubSquadEntry{ElementID: bogleID, Name: "Bogle", CaptainScore: 12.0, EPNext: 6.8},
	)
	if got := captainChoice(squad, allFour); got.Name != "Saka" {
		t.Errorf("captainChoice = %s (%s), want Saka", got.Name, got.Reason)
	}
}

func TestCaptainChoiceTiebreak(t *testing.T) {
	t.Run("within 5%, higher ep_next wins", func(t *testing.T) {
		squad := asStarters(
			HubSquadEntry{ElementID: sakaID, Name: "Saka", CaptainScore: 12.4, EPNext: 6.1},
			HubSquadEntry{ElementID: haalandID, Name: "Haaland", CaptainScore: 12.0, EPNext: 7.9},
		)
		got := captainChoice(squad, allFour)
		if got.Name != "Haaland" {
			t.Fatalf("captainChoice = %s, want Haaland", got.Name)
		}
		for _, want := range []string{"within 5%", "Saka", "7.9", "6.1"} {
			if !strings.Contains(got.Reason, want) {
				t.Errorf("reason %q is missing %q", got.Reason, want)
			}
		}
	})

	t.Run("more than 5% clear, captain_score wins", func(t *testing.T) {
		squad := asStarters(
			HubSquadEntry{ElementID: sakaID, Name: "Saka", CaptainScore: 12.4, EPNext: 6.1},
			HubSquadEntry{ElementID: haalandID, Name: "Haaland", CaptainScore: 11.0, EPNext: 7.9},
		)
		got := captainChoice(squad, allFour)
		if got.Name != "Saka" {
			t.Fatalf("captainChoice = %s, want Saka", got.Name)
		}
		for _, want := range []string{"more than 5% clear", "Haaland", "7.9"} {
			if !strings.Contains(got.Reason, want) {
				t.Errorf("reason %q is missing %q", got.Reason, want)
			}
		}
	})

	t.Run("an ep_next tie keeps the captain_score leader", func(t *testing.T) {
		squad := asStarters(
			HubSquadEntry{ElementID: sakaID, Name: "Saka", CaptainScore: 12.4, EPNext: 7.0},
			HubSquadEntry{ElementID: haalandID, Name: "Haaland", CaptainScore: 12.0, EPNext: 7.0},
		)
		got := captainChoice(squad, allFour)
		if got.Name != "Saka" || !strings.Contains(got.Reason, "leads both") {
			t.Errorf("captainChoice = %s (%q), want Saka leading both", got.Name, got.Reason)
		}
	})
}

func TestCaptainChoiceEdgeCases(t *testing.T) {
	t.Run("agreement", func(t *testing.T) {
		squad := asStarters(
			HubSquadEntry{ElementID: sakaID, Name: "Saka", CaptainScore: 12.4, EPNext: 7.5},
			HubSquadEntry{ElementID: bogleID, Name: "Bogle", CaptainScore: 8.9, EPNext: 6.8},
		)
		got := captainChoice(squad, allFour)
		if got.Name != "Saka" || !strings.Contains(got.Reason, "leads both") {
			t.Errorf("captainChoice = %s (%q), want Saka leading both", got.Name, got.Reason)
		}
	})

	t.Run("bench players are not candidates", func(t *testing.T) {
		squad := asStarters(HubSquadEntry{ElementID: sakaID, Name: "Saka", CaptainScore: 12.4, EPNext: 6.1})
		squad = append(squad, HubSquadEntry{Slot: 13, ElementID: haalandID, Name: "Haaland", CaptainScore: 15, EPNext: 9})
		if got := captainChoice(squad, allFour); got.Name != "Saka" {
			t.Errorf("captainChoice = %s, want Saka: a benched player can't be captain", got.Name)
		}
	})

	t.Run("preseason, no ep_next", func(t *testing.T) {
		squad := asStarters(
			HubSquadEntry{ElementID: sakaID, Name: "Saka", CaptainScore: 12.4},
			HubSquadEntry{ElementID: haalandID, Name: "Haaland", CaptainScore: 12.0},
		)
		got := captainChoice(squad, allFour)
		if got.Name != "Saka" || !strings.Contains(got.Reason, "no ep_next") {
			t.Errorf("captainChoice = %s (%q), want Saka with no ep_next", got.Name, got.Reason)
		}
	})

	t.Run("too few minutes is not attacking threat", func(t *testing.T) {
		newSigning := fpl.Player{ID: 99, Minutes: 180, ExpectedGoalInvolvements: 2.0}
		squad := asStarters(
			HubSquadEntry{ElementID: sakaID, Name: "Saka", CaptainScore: 12.4, EPNext: 6.1},
			HubSquadEntry{ElementID: 99, Name: "NewSigning", CaptainScore: 12.2, EPNext: 8.0},
		)
		got := captainChoice(squad, captainPlayers(pSaka, newSigning))
		if got.Name != "Saka" || !strings.Contains(got.Reason, "under 270 minutes") {
			t.Errorf("captainChoice = %s (%q), want Saka, citing too few minutes", got.Name, got.Reason)
		}
	})

	t.Run("no starters", func(t *testing.T) {
		if got := captainChoice(nil, allFour); got != nil {
			t.Errorf("captainChoice(nil) = %+v, want nil", got)
		}
	})
}

// End to end: the hub reports one captain from the manager's starters.
func TestManagerHubCaptainChoiceWired(t *testing.T) {
	e := hubEngine(t)
	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got.CaptainChoice == nil {
		t.Fatal("CaptainChoice is nil")
	}
	var found bool
	for _, s := range got.Squad {
		if s.ElementID == got.CaptainChoice.ElementID {
			found = s.Starter
		}
	}
	if !found {
		t.Errorf("CaptainChoice %s is not one of the starters", got.CaptainChoice.Name)
	}
	if got.CaptainChoice.Reason == "" {
		t.Error("CaptainChoice has no reason")
	}
}
