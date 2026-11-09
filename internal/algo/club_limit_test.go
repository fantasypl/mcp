package algo

import (
	"context"
	"strings"
	"testing"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Issue #43: the single-swap tools recommended players the caller could not
// legally buy, because the squad already held three from that club.
//
// The synthetic squad (picks_squad1.json) holds three Arsenal players
// (Gabriel, Rice, J.Timber) and three Aston Villa players (Konsa, Digne,
// A.García), the same shape as the issue's teams 135586 and 2233768.

// squadClubs returns element ID -> club, and club -> count, for the
// synthetic squad.
func squadClubs(t *testing.T, e *Engine) (map[int]int, map[int]int) {
	t.Helper()
	ctx := context.Background()
	b, _ := e.client.Bootstrap(ctx)
	picks, err := e.client.TeamPicks(ctx, syntheticTeamID, b.NextGameweek())
	if err != nil {
		t.Fatal(err)
	}
	clubOf := make(map[int]int, len(b.Elements))
	for _, p := range b.Elements {
		clubOf[p.ID] = p.Team
	}
	counts := make(map[int]int)
	for _, pk := range picks.Picks {
		counts[clubOf[pk.Element]]++
	}
	return clubOf, counts
}

func allSquadIDs(t *testing.T, e *Engine) []int {
	t.Helper()
	b, _ := e.client.Bootstrap(context.Background())
	picks, _ := e.client.TeamPicks(context.Background(), syntheticTeamID, b.NextGameweek())
	ids := make([]int, 0, len(picks.Picks))
	for _, pk := range picks.Picks {
		ids = append(ids, pk.Element)
	}
	return ids
}

// Every in-option must be legal as a single transfer: after selling the
// transfer_out player and buying the option, no club has more than three.
func TestTransferSuggestionsRespectClubLimit(t *testing.T) {
	e := hubEngine(t)
	clubOf, counts := squadClubs(t, e)

	// Ask for a replacement for every squad player, so every club's limit
	// is exercised.
	out, err := e.TransferSuggestionsIncluding(context.Background(), syntheticTeamID, 1, 2.0, allSquadIDs(t, e))
	if err != nil {
		t.Fatal(err)
	}
	res := out.(*TransferSuggestionsResult)
	if len(res.TransferSuggestions) != 15 {
		t.Fatalf("got %d suggestions, want one per squad player (15)", len(res.TransferSuggestions))
	}

	for _, s := range res.TransferSuggestions {
		for _, in := range s.TransferInOptions {
			after := counts[clubOf[in.ID]] + 1
			if clubOf[in.ID] == clubOf[s.TransferOut.ID] {
				after-- // a like-for-like swap within one club
			}
			if after > maxPlayersPerClub {
				t.Errorf("selling %s (%s): option %s (%s) makes %d from one club",
					s.TransferOut.Name, s.TransferOut.Team, in.Name, in.Team, after)
			}
		}
	}
}

// club_limit_ok marks options that are legal on their own but break the
// limit together with the top picks of the suggestions listed before them.
func TestTransferSuggestionsClubLimitOKAcrossTheResponse(t *testing.T) {
	e := hubEngine(t)
	clubOf, counts := squadClubs(t, e)
	out, err := e.TransferSuggestionsIncluding(context.Background(), syntheticTeamID, 1, 2.0, allSquadIDs(t, e))
	if err != nil {
		t.Fatal(err)
	}
	res := out.(*TransferSuggestionsResult)

	// Replay the plan the field describes: sell each transfer_out in turn and
	// buy that suggestion's first option marked club_limit_ok.
	plan := make(map[int]int, len(counts))
	for club, n := range counts {
		plan[club] = n
	}
	sawFlagged := false
	for _, s := range res.TransferSuggestions {
		plan[clubOf[s.TransferOut.ID]]--
		bought := false
		for _, in := range s.TransferInOptions {
			legal := plan[clubOf[in.ID]]+1 <= maxPlayersPerClub
			if in.ClubLimitOK == nil {
				t.Fatalf("option %s has no club_limit_ok", in.Name)
			}
			if *in.ClubLimitOK != legal {
				t.Errorf("selling %s: option %s (%s) club_limit_ok = %v, want %v (club count before buying %d)",
					s.TransferOut.Name, in.Name, in.Team, *in.ClubLimitOK, legal, plan[clubOf[in.ID]])
			}
			if !legal {
				sawFlagged = true
				if !strings.Contains(in.ClubLimitNote, in.Team) {
					t.Errorf("option %s: club_limit_note %q should name the club", in.Name, in.ClubLimitNote)
				}
			}
			if legal && !bought {
				plan[clubOf[in.ID]]++
				bought = true
			}
		}
	}
	if !sawFlagged {
		t.Errorf("expected at least one option flagged across 15 replacements in a squad with two full clubs")
	}
}

// The hub's differential_targets ranked a Brighton player first for a squad
// already holding three from Brighton. Differentials from a full club stay
// listed (selling one of the three makes room), but are flagged, say who would
// have to go, and rank after the legal ones.
func TestHubDifferentialTargetsFlagFullClubs(t *testing.T) {
	e := hubEngine(t)
	stub := e.client.(*StubClient)
	_, counts := squadClubs(t, e)

	// Make an Arsenal player (a full club in the squad) the clear top
	// differential: tiny ownership, outstanding output.
	var arsenal int
	for club, n := range counts {
		if n == maxPlayersPerClub {
			arsenal = club
			break
		}
	}
	var target *fpl.Player
	for i := range stub.bootstrap.Elements {
		p := &stub.bootstrap.Elements[i]
		if p.Team == arsenal && p.ID != 4 && p.ID != 13 && p.ID != 5 && p.ElementType == 3 {
			target = p
			break
		}
	}
	target.SelectedByPercent = numOf(1.0)
	target.Form, target.PointsPerGame = numOf(12), numOf(9)
	target.ExpectedGoals, target.ExpectedAssists = numOf(15), numOf(10)
	target.Status = "a"

	got, err := e.ManagerHub(context.Background(), syntheticTeamID, 5)
	if err != nil {
		t.Fatal(err)
	}
	seenFlagged := false
	for _, d := range got.DifferentialTargets {
		if d.ClubLimitOK == nil {
			t.Fatalf("differential %s has no club_limit_ok", d.Player.Name)
		}
		if !*d.ClubLimitOK {
			seenFlagged = true
		} else if seenFlagged {
			t.Errorf("legal differential %s ranks after a flagged one", d.Player.Name)
		}
		if d.Player.ID == target.ID {
			if *d.ClubLimitOK {
				t.Errorf("%s is from a club the squad holds 3 of, but club_limit_ok is true", d.Player.Name)
			}
			for _, name := range []string{"Gabriel", "Rice", "J.Timber"} {
				if !strings.Contains(d.ClubLimitNote, name) {
					t.Errorf("club_limit_note %q should name %s as a player to sell", d.ClubLimitNote, name)
				}
			}
		}
	}
	if !seenFlagged {
		t.Fatalf("the boosted Arsenal differential %s was not flagged: %+v", target.WebName, got.DifferentialTargets)
	}
}

// player_comparison with a team_id: the issue's case was a squad holding three
// Arsenal players, none of them in the compared player's position, so no
// single transfer could make room, yet the verdict gave him the edge. Here
// the squad holds Gabriel, J.Timber (DEF) and Rice (MID), and Gyokeres is an
// Arsenal forward.
func TestComparePlayersForTeamExcludesIllegalBuys(t *testing.T) {
	const gyokeresID, kostoulasID = 25, 138
	e := hubEngine(t)
	stub := e.client.(*StubClient)
	for _, id := range []int{gyokeresID, kostoulasID} {
		stub.SetPlayerSummary(id, &fpl.PlayerSummary{})
	}
	// Make Gyokeres the runaway choice on output, so only legality can stop
	// him.
	for i := range stub.bootstrap.Elements {
		if stub.bootstrap.Elements[i].ID == gyokeresID {
			stub.bootstrap.Elements[i].Form = numOf(15)
			stub.bootstrap.Elements[i].PointsPerGame = numOf(12)
		}
	}
	teamID := syntheticTeamID
	got, err := e.ComparePlayersForTeam(context.Background(), []string{"Gyokeres", "Kostoulas"}, 5, &teamID)
	if err != nil {
		t.Fatal(err)
	}
	var gyo, kos *PlayerProfile
	for i := range got.Players {
		switch got.Players[i].ID {
		case gyokeresID:
			gyo = &got.Players[i]
		case kostoulasID:
			kos = &got.Players[i]
		}
	}
	if gyo == nil || kos == nil {
		t.Fatalf("players = %+v, want Gyokeres and Kostoulas", got.Players)
	}
	if gyo.Legality == nil || gyo.Legality.ClubLimitOK || gyo.Legality.LegalBuy {
		t.Fatalf("Gyokeres legality = %+v, want club_limit_ok and legal_buy false", gyo.Legality)
	}
	if kos.Legality == nil || !kos.Legality.LegalBuy {
		t.Errorf("Kostoulas legality = %+v, want a legal buy", kos.Legality)
	}
	first := strings.SplitN(got.Verdict, " Left out", 2)[0]
	if strings.Contains(first, "Gy") {
		t.Errorf("verdict %q considers Gyokeres, who is not a legal buy", got.Verdict)
	}
	if !strings.Contains(got.Verdict, "Left out") || !strings.Contains(got.Verdict, "3-per-club") {
		t.Errorf("verdict %q should say Gyokeres was left out for the 3-per-club rule", got.Verdict)
	}

	// Without a team_id nothing changes: no legality, and Gyokeres wins.
	plain, err := e.ComparePlayers(context.Background(), []string{"Gyokeres", "Kostoulas"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plain.Players {
		if p.Legality != nil {
			t.Errorf("%s has legality without a team_id", p.Name)
		}
	}
	if !strings.HasPrefix(plain.Verdict, "Gy") {
		t.Errorf("plain verdict %q, want Gyokeres first without a team_id", plain.Verdict)
	}
}

// buyLegality's branches, on a hand-built squad.
func TestBuyLegality(t *testing.T) {
	teams := map[int]*fpl.Team{1: {ID: 1, ShortName: "ARS"}, 2: {ID: 2, ShortName: "AVL"}}
	mk := func(id, club, pos, cost int, name string) *fpl.Player {
		return &fpl.Player{ID: id, Team: club, ElementType: pos, NowCost: cost, WebName: name}
	}
	squad := []*fpl.Player{
		mk(1, 1, 2, 60, "Gabriel"), mk(2, 1, 2, 55, "Timber"), mk(3, 1, 3, 75, "Rice"),
		mk(4, 2, 4, 80, "Watkins"),
	}
	cases := []struct {
		name      string
		p         *fpl.Player
		bank      int
		wantLegal bool
		wantClub  bool
		wantNote  string
	}{
		{"owned", squad[0], 0, true, true, "Already in your squad"},
		{"full club, same position swap", mk(10, 1, 2, 50, "Saliba"), 0, true, true, "selling Gabriel or Timber"},
		{"full club, no same-position team-mate", mk(11, 1, 4, 70, "Gyokeres"), 50, false, false, "3-per-club"},
		{"over budget", mk(12, 2, 4, 150, "Haaland"), 5, false, true, "covers 15.0m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buyLegality(tc.p, squad, nil, tc.bank, teams)
			if got.LegalBuy != tc.wantLegal || got.ClubLimitOK != tc.wantClub {
				t.Errorf("legality = %+v, want legal_buy %v, club_limit_ok %v", got, tc.wantLegal, tc.wantClub)
			}
			if !strings.Contains(got.Note, tc.wantNote) {
				t.Errorf("note %q, want it to contain %q", got.Note, tc.wantNote)
			}
		})
	}
}
