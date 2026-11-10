package algo

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/fantasypl/mcp/internal/fpl"
)

// LeagueRef is a mini-league the user saved as one they compete in
// (FPL_MCP_LEAGUES). Label is optional, such as "Work".
type LeagueRef struct {
	ID    int
	Label string
}

const (
	// A league this size or smaller counts every manager as a rival.
	fieldWholeLeagueMax = 20
	// In a bigger league, the managers within this many places of the
	// caller are the rivals.
	fieldWindow = 10
	// maxFieldRivals caps the squads read across all saved leagues.
	maxFieldRivals = 60

	shieldCaptainedPct   = 40.0
	templateOwnedPct     = 50.0
	differentialOwnedPct = 15.0

	StanceShield       = "shield"
	StanceTemplate     = "template"
	StanceDifferential = "differential"
	StanceMixed        = "mixed"
)

// LeagueOwnership is how a player is held across the caller's rivals in
// their saved leagues. See leagueOwnershipBasis.
type LeagueOwnership struct {
	Rivals                int     `json:"rivals"`
	OwnedBy               int     `json:"owned_by"`
	OwnedPct              float64 `json:"owned_pct"`
	CaptainedBy           int     `json:"captained_by"`
	CaptainedPct          float64 `json:"captained_pct"`
	EffectiveOwnershipPct float64 `json:"effective_ownership_pct"`
	Stance                string  `json:"stance"`
}

// leagueOwnershipBasis explains league_ownership in words, for the result.
func leagueOwnershipBasis(picksGW int) string {
	return fmt.Sprintf("league_ownership counts your rivals in your saved leagues: every manager in a league of %d or fewer, otherwise the %d places above and below you, at most %d rivals in all and each counted once. "+
		"It reads each rival's latest public squad (gameweek %d), so transfers made since that deadline are not seen. "+
		"owned_pct is the share of rivals with the player in their 15, captained_pct the share who captained him, and effective_ownership_pct the average multiplier across rivals (starter 1, captain 2, Triple Captain 3) times 100. "+
		"stance: shield when %.0f%% or more of rivals captained him, so captaining him protects your position; template when %.0f%% or more own him, so not owning him is a bet against him; differential when %.0f%% or fewer own him, so his points gain you ground; mixed otherwise. "+
		"It is information and does not change any ranking.",
		fieldWholeLeagueMax, fieldWindow, maxFieldRivals, picksGW, shieldCaptainedPct, templateOwnedPct, differentialOwnedPct)
}

// ownershipTally counts holdings over a set of rival squads.
type ownershipTally struct {
	rivals    int
	owned     map[int]int
	captained map[int]int
	weight    map[int]int // sum of multipliers, for effective ownership
}

func newTally() *ownershipTally {
	return &ownershipTally{owned: map[int]int{}, captained: map[int]int{}, weight: map[int]int{}}
}

func (t *ownershipTally) add(picks []fpl.Pick) {
	t.rivals++
	for _, p := range picks {
		t.owned[p.Element]++
		if p.Position > 11 {
			continue
		}
		m := 1
		if p.IsCaptain {
			t.captained[p.Element]++
			m = 2
			if p.Multiplier == 3 {
				m = 3
			}
		}
		t.weight[p.Element] += m
	}
}

func pct(n, of int) float64 {
	if of == 0 {
		return 0
	}
	return Round(100*float64(n)/float64(of), 1)
}

func (t *ownershipTally) of(playerID int) *LeagueOwnership {
	if t == nil || t.rivals == 0 {
		return nil
	}
	o := &LeagueOwnership{
		Rivals: t.rivals, OwnedBy: t.owned[playerID], CaptainedBy: t.captained[playerID],
		OwnedPct: pct(t.owned[playerID], t.rivals), CaptainedPct: pct(t.captained[playerID], t.rivals),
		EffectiveOwnershipPct: pct(t.weight[playerID], t.rivals),
	}
	switch {
	case o.CaptainedPct >= shieldCaptainedPct:
		o.Stance = StanceShield
	case o.OwnedPct >= templateOwnedPct:
		o.Stance = StanceTemplate
	case o.OwnedPct <= differentialOwnedPct:
		o.Stance = StanceDifferential
	default:
		o.Stance = StanceMixed
	}
	return o
}

// FieldLeague is one saved league as LeagueField read it.
type FieldLeague struct {
	Ref       LeagueRef
	Name      string
	Standings []fpl.LeagueEntry
	HasNext   bool
	You       *fpl.LeagueEntry // nil when the caller is not on the first standings page
	RivalIDs  []int            // the rivals whose squads were read
	Note      string
	tally     *ownershipTally
}

// RivalField is the caller's rivals across every saved league: who they
// are and what they own.
type RivalField struct {
	TeamID        int
	PicksGameweek int
	Leagues       []*FieldLeague
	Missing       int // rival squads that could not be fetched
	all           *ownershipTally
}

// Ownership is a player's holding across every rival, each counted once.
// Nil when no rival squad was read.
func (f *RivalField) Ownership(playerID int) *LeagueOwnership {
	if f == nil {
		return nil
	}
	return f.all.of(playerID)
}

// Basis is the league_ownership_basis text, or "" when nothing was counted.
func (f *RivalField) Basis() string {
	if f == nil || f.all == nil || f.all.rivals == 0 {
		return ""
	}
	return leagueOwnershipBasis(f.PicksGameweek)
}

// pickRivals chooses a league's rivals: everyone in a small league, else the
// managers within fieldWindow places, else (caller not on the first page)
// the top of the table.
func pickRivals(standings []fpl.LeagueEntry, hasNext bool, teamID int) (rivals []fpl.LeagueEntry, you *fpl.LeagueEntry, note string) {
	youIdx := -1
	for i := range standings {
		if standings[i].Entry == teamID {
			youIdx = i
			you = &standings[i]
			break
		}
	}
	switch {
	case !hasNext && len(standings) <= fieldWholeLeagueMax:
		for _, s := range standings {
			if s.Entry != teamID {
				rivals = append(rivals, s)
			}
		}
		if you == nil {
			note = fmt.Sprintf("Team %d is not in this league, so every manager in it is counted as a rival but your own position is not shown.", teamID)
		}
	case youIdx >= 0:
		start, end := max(0, youIdx-fieldWindow), min(len(standings), youIdx+fieldWindow+1)
		for _, s := range standings[start:end] {
			if s.Entry != teamID {
				rivals = append(rivals, s)
			}
		}
	default:
		rivals = standings[:min(len(standings), 2*fieldWindow)]
		note = fmt.Sprintf("Team %d is not on the first page of this league's standings, so the top %d are counted as rivals.", teamID, len(rivals))
	}
	return rivals, you, note
}

// LeagueField reads the caller's saved leagues and their rivals' latest
// squads. It is best-effort: a league or squad that cannot be fetched is
// noted and skipped. It returns nil when no leagues are saved.
func (e *Engine) LeagueField(ctx context.Context, teamID int) *RivalField {
	if len(e.Leagues) == 0 {
		return nil
	}
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil
	}
	f := &RivalField{TeamID: teamID, PicksGameweek: bootstrap.CurrentGameweek(), Leagues: make([]*FieldLeague, len(e.Leagues)), all: newTally()}

	g, gctx := errgroup.WithContext(ctx)
	for i, ref := range e.Leagues {
		g.Go(func() error {
			fl := &FieldLeague{Ref: ref, tally: newTally()}
			f.Leagues[i] = fl
			s, err := e.client.LeagueStandings(gctx, ref.ID)
			if err != nil || len(s.Standings.Results) == 0 {
				fl.Note = fmt.Sprintf("Could not read league %d. Check the ID in your saved leagues.", ref.ID)
				return nil
			}
			fl.Name, fl.Standings, fl.HasNext = s.League.Name, s.Standings.Results, s.Standings.HasNext
			return nil
		})
	}
	_ = g.Wait() // every goroutine returns nil

	if f.PicksGameweek < 1 {
		for _, fl := range f.Leagues {
			if fl.Note == "" {
				fl.Note = "The season has not started, so there are no rival squads to read yet."
			}
		}
		return f
	}

	// Choose each league's rivals, then read every distinct squad once.
	wanted := map[int]bool{}
	var order []int
	chosen := make([][]fpl.LeagueEntry, len(f.Leagues))
	for i, fl := range f.Leagues {
		if fl.Standings == nil {
			continue
		}
		rivals, you, note := pickRivals(fl.Standings, fl.HasNext, teamID)
		fl.You, fl.Note = you, note
		for _, r := range rivals {
			if !wanted[r.Entry] {
				if len(order) == maxFieldRivals {
					continue
				}
				wanted[r.Entry] = true
				order = append(order, r.Entry)
			}
			chosen[i] = append(chosen[i], r)
		}
	}

	var mu sync.Mutex
	squads := make(map[int][]fpl.Pick, len(order))
	g, gctx = errgroup.WithContext(ctx)
	g.SetLimit(8)
	for _, id := range order {
		g.Go(func() error {
			p, err := e.client.TeamPicks(gctx, id, f.PicksGameweek)
			if err != nil {
				return nil // best-effort
			}
			mu.Lock()
			squads[id] = p.Picks
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	f.Missing = len(order) - len(squads)

	for _, id := range order {
		if picks, ok := squads[id]; ok {
			f.all.add(picks)
		}
	}
	for i, fl := range f.Leagues {
		for _, r := range chosen[i] {
			if picks, ok := squads[r.Entry]; ok {
				fl.tally.add(picks)
				fl.RivalIDs = append(fl.RivalIDs, r.Entry)
			}
		}
	}
	return f
}

// mostOwnedOutside lists the players most owned by a league's rivals that
// are not in squad, highest effective ownership first.
func (fl *FieldLeague) mostOwnedOutside(squad map[int]bool, limit int) []int {
	var ids []int
	for id, n := range fl.tally.owned {
		if !squad[id] && pct(n, fl.tally.rivals) >= templateOwnedPct {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := fl.tally.weight[ids[i]], fl.tally.weight[ids[j]]
		if a != b {
			return a > b
		}
		return ids[i] < ids[j]
	})
	return ids[:min(len(ids), limit)]
}

// WithLeagueOwnership adds league_ownership to a tool result's players,
// measured against teamID's rivals in the saved leagues. Results it does not
// know, and calls with no saved leagues, pass through unchanged.
func (e *Engine) WithLeagueOwnership(ctx context.Context, teamID int, out any) any {
	if len(e.Leagues) == 0 || teamID == 0 {
		return out
	}
	switch out.(type) {
	case *CaptainResult, *TransferSuggestionsResult, *OptimalTransfersResult, *WildcardResult:
	default:
		return out
	}
	f := e.LeagueField(ctx, teamID)
	basis := f.Basis()
	if basis == "" {
		return out
	}
	switch r := out.(type) {
	case *CaptainResult:
		f.annotateCaptainPicks(r.Picks)
		r.LeagueOwnershipBasis = basis
	case *TransferSuggestionsResult:
		f.annotateTransfers(r.TransferSuggestions)
		r.LeagueOwnershipBasis = basis
	case *OptimalTransfersResult:
		for i := range r.Options {
			f.annotateSlots(r.Options[i].TransfersIn)
			f.annotateSlots(r.Options[i].TransfersOut)
		}
		r.LeagueOwnershipBasis = basis
	case *WildcardResult:
		f.annotateSlots(r.TransfersIn)
		f.annotateSlots(r.TransfersOut)
		r.LeagueOwnershipBasis = basis
	}
	return out
}

func (f *RivalField) annotateCaptainPicks(picks []CaptainPick) {
	for i := range picks {
		picks[i].LeagueOwnership = f.Ownership(picks[i].Player.ID)
	}
}

func (f *RivalField) annotateTransfers(ts []TransferSuggestion) {
	for i := range ts {
		ts[i].TransferOut.LeagueOwnership = f.Ownership(ts[i].TransferOut.ID)
		for j := range ts[i].TransferInOptions {
			ts[i].TransferInOptions[j].LeagueOwnership = f.Ownership(ts[i].TransferInOptions[j].ID)
		}
	}
}

func (f *RivalField) annotateSlots(slots []OptimalSquadSlot) {
	for i := range slots {
		slots[i].LeagueOwnership = f.Ownership(slots[i].ID)
	}
}
