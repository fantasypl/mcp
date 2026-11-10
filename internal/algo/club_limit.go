package algo

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/fantasypl/mcp/internal/fpl"
)

// The 3-per-club rule for the single-swap tools (issue #43).
//
// optimal_squad and optimal_transfers enforce FPL's limit of three players
// from one club inside the branch-and-bound search. transfer_suggestions, the
// hub's differential_targets and player_comparison did not, so they could
// recommend a fourth Arsenal player to a squad that already had three.
//
// Each tool applies the rule at the strength its output allows:
//
//   - transfer_suggestions knows the player going out, so an in-option that
//     breaks the limit after that one transfer is dropped. An option that is
//     legal alone but breaks it together with the top picks of the earlier
//     suggestions in the same response stays listed, marked
//     club_limit_ok: false, because picking a different earlier option
//     fixes it.
//   - differential_targets has no player going out. A differential from a
//     club the squad already holds three of is still buyable as a swap for
//     one of those three, so it is flagged, names who would have to go, and
//     ranks after the legal ones.
//   - player_comparison with a team_id checks every same-position sale and
//     reports whether any of them makes the player a legal, affordable buy.
//     Players who are not are left out of the verdict.

// maxPlayersPerClub is FPL's limit on players from one Premier League club.
const maxPlayersPerClub = 3

// clubCounts is the number of squad players from each club, keyed by FPL
// team ID.
type clubCounts map[int]int

// countClubs counts a squad's players by club.
func countClubs(players []*fpl.Player) clubCounts {
	counts := make(clubCounts, len(players))
	for _, p := range players {
		counts[p.Team]++
	}
	return counts
}

// copyCounts returns an independent copy, for simulating a plan of transfers.
func (c clubCounts) copyCounts() clubCounts {
	out := make(clubCounts, len(c))
	for club, n := range c {
		out[club] = n
	}
	return out
}

// swapLegal reports whether selling a player from outClub and buying one from
// inClub keeps every club within the limit.
func (c clubCounts) swapLegal(outClub, inClub int) bool {
	after := c[inClub] + 1
	if inClub == outClub {
		after-- // like for like within one club never changes the count
	}
	return after <= maxPlayersPerClub
}

// markResponseClubLimits sets club_limit_ok on every in-option of a
// transfer_suggestions response, treating the suggestions as one plan: each
// transfer_out is sold in turn, and each suggestion's first legal option is
// bought before the next suggestion is checked.
//
// Options were already filtered to be legal as single transfers, so a false
// here always means "legal alone, but not alongside the earlier top picks".
func markResponseClubLimits(suggestions []TransferSuggestion, squad clubCounts, byID map[int]*fpl.Player, teams map[int]*fpl.Team) {
	plan := squad.copyCounts()
	// boughtFrom lists the earlier top picks by club, to name them in notes.
	boughtFrom := make(map[int][]string)

	for si := range suggestions {
		s := &suggestions[si]
		if out := byID[s.TransferOut.ID]; out != nil {
			plan[out.Team]--
		}
		bought := false
		for oi := range s.TransferInOptions {
			opt := &s.TransferInOptions[oi]
			p := byID[opt.ID]
			if p == nil {
				continue
			}
			ok := plan[p.Team]+1 <= maxPlayersPerClub
			opt.ClubLimitOK = ptr(ok)
			if !ok {
				opt.ClubLimitNote = fmt.Sprintf(
					"Legal on its own, but with the top picks listed earlier in this response (%s) it would make %d from %s.",
					strings.Join(boughtFrom[p.Team], ", "), plan[p.Team]+1, shortName(teams[p.Team]))
				continue
			}
			if !bought {
				plan[p.Team]++
				boughtFrom[p.Team] = append(boughtFrom[p.Team], opt.Name)
				bought = true
			}
		}
	}
}

// flagDifferentialClubLimits sets club_limit_ok on the hub's differential
// targets for one squad and moves flagged targets after the legal ones,
// keeping each group's order. Ranks are renumbered to match.
func flagDifferentialClubLimits(diffs []Differential, squad []*fpl.Player, byID map[int]*fpl.Player, teams map[int]*fpl.Team) []Differential {
	counts := countClubs(squad)
	var legal, flagged []Differential
	for _, d := range diffs {
		p := byID[d.Player.ID]
		if p == nil || counts[p.Team] < maxPlayersPerClub {
			d.ClubLimitOK = ptr(true)
			legal = append(legal, d)
			continue
		}
		var sameClub []string
		for _, s := range squad {
			if s.Team == p.Team {
				sameClub = append(sameClub, s.WebName)
			}
		}
		d.ClubLimitOK = ptr(false)
		d.ClubLimitNote = fmt.Sprintf("Your squad already has %d from %s (%s). Buying %s means selling one of them.",
			counts[p.Team], shortName(teams[p.Team]), strings.Join(sameClub, ", "), p.WebName)
		flagged = append(flagged, d)
	}
	out := append(legal, flagged...)
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

// BuyLegality says whether a compared player can join the caller's squad
// with one transfer. Set on player_comparison results only when a team_id is
// given.
type BuyLegality struct {
	// InSquad is true when the manager already owns the player. Such a
	// player is eligible for the verdict, since keeping him needs no
	// transfer.
	InSquad bool `json:"in_squad"`
	// ClubLimitOK is true when at least one same-position sale keeps every
	// club within three.
	ClubLimitOK bool `json:"club_limit_ok"`
	// BudgetOK is true when at least one same-position sale, at its selling
	// price, plus the bank covers the player's price.
	BudgetOK bool `json:"budget_ok"`
	// LegalBuy is true when one sale satisfies both at once, or the player
	// is already owned.
	LegalBuy bool   `json:"legal_buy"`
	Note     string `json:"note"`
}

// ComparePlayersForTeam is player_comparison with an optional team_id. With
// no team, it is ComparePlayers. With one, each player gains legality, and
// the verdict only considers players the manager could have next gameweek.
func (e *Engine) ComparePlayersForTeam(ctx context.Context, names []string, gameweeksAhead int, teamID *int) (*CompareResult, error) {
	res, err := e.ComparePlayers(ctx, names, gameweeksAhead)
	if err != nil || teamID == nil || res.kind != compareKindSuccess {
		return res, err
	}

	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	picks, err := e.client.TeamPicks(ctx, *teamID, bootstrap.NextGameweek())
	if err != nil {
		// Before the deadline the next gameweek's picks do not exist yet.
		picks, err = e.client.TeamPicks(ctx, *teamID, bootstrap.CurrentGameweek())
		if err != nil {
			return compareError(fmt.Sprintf("Could not fetch picks for team %d. Check the team ID is correct.", *teamID)), nil
		}
	}

	byID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		byID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)
	squad := make([]*fpl.Player, 0, len(picks.Picks))
	for _, pk := range picks.Picks {
		if p := byID[pk.Element]; p != nil {
			squad = append(squad, p)
		}
	}
	// Selling prices when the transfer history is available, otherwise
	// market prices, as transfer_suggestions does.
	selling := e.squadSellingPrices(ctx, *teamID, squad)
	bankTenths := picks.EntryHistory.Bank

	var eligible []PlayerProfile
	var excluded []string
	for i := range res.Players {
		prof := &res.Players[i]
		p := byID[prof.ID]
		if p == nil {
			continue
		}
		prof.Legality = buyLegality(p, squad, selling, bankTenths, teams)
		if prof.Legality.LegalBuy {
			eligible = append(eligible, *prof)
		} else {
			excluded = append(excluded, fmt.Sprintf("%s (%s)", prof.Name, legalityReason(prof.Legality)))
		}
	}

	switch {
	case len(eligible) == 0:
		res.Verdict = fmt.Sprintf("None of these players is a legal buy for team %d with one transfer: %s.", *teamID, strings.Join(excluded, "; "))
	case len(excluded) == 0:
		res.Verdict = buildVerdict(eligible)
	default:
		res.Verdict = buildVerdict(eligible) + fmt.Sprintf(" Left out of the verdict as not a legal buy for team %d: %s.", *teamID, strings.Join(excluded, "; "))
	}
	return res, nil
}

// buyLegality checks one player against a squad. Every same-position squad
// player is a possible sale.
func buyLegality(p *fpl.Player, squad []*fpl.Player, selling map[int]SquadPrice, bankTenths int, teams map[int]*fpl.Team) *BuyLegality {
	for _, s := range squad {
		if s.ID == p.ID {
			return &BuyLegality{InSquad: true, ClubLimitOK: true, BudgetOK: true, LegalBuy: true, Note: "Already in your squad."}
		}
	}

	counts := countClubs(squad)
	out := &BuyLegality{}
	var legalSales []string
	for _, s := range squad {
		if s.ElementType != p.ElementType {
			continue
		}
		clubOK := counts.swapLegal(s.Team, p.Team)
		price := s.NowCost
		if sp, ok := selling[s.ID]; ok {
			price = sp.SellingTenths
		}
		budgetOK := bankTenths+price >= p.NowCost
		out.ClubLimitOK = out.ClubLimitOK || clubOK
		out.BudgetOK = out.BudgetOK || budgetOK
		if clubOK && budgetOK {
			legalSales = append(legalSales, s.WebName)
		}
	}
	out.LegalBuy = len(legalSales) > 0

	club := shortName(teams[p.Team])
	switch {
	case out.LegalBuy:
		slices.Sort(legalSales)
		out.Note = fmt.Sprintf("Legal buy by selling %s.", strings.Join(legalSales, " or "))
	case !out.ClubLimitOK:
		out.Note = fmt.Sprintf("Your squad already has %d from %s and none of them plays %s, so no single transfer makes room (3-per-club limit).",
			counts[p.Team], club, Position(p.ElementType))
	case !out.BudgetOK:
		out.Note = fmt.Sprintf("No single %s sale plus your bank covers %.1fm.", Position(p.ElementType), float64(p.NowCost)/10)
	default:
		out.Note = "The sales that keep the 3-per-club limit don't free enough money, and the ones that free enough break it."
	}
	return out
}

// legalityReason is the short reason a player was left out of the verdict.
func legalityReason(l *BuyLegality) string {
	switch {
	case !l.ClubLimitOK:
		return "3-per-club limit"
	case !l.BudgetOK:
		return "over budget"
	default:
		return "no sale satisfies both the 3-per-club limit and the budget"
	}
}
