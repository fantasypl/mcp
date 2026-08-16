package algo

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Squad Scout surfaces the parts of FPL's own data that most managers never
// look at: its own expected-points prediction (ep_next), value and
// consistency metrics, exactly who is on which set piece, and card-based
// suspension risk computed from the Premier League's actual yellow-card
// thresholds. None of this is opinion — it's the FPL API's own numbers,
// organised into a report on a specific squad.

// yellowThreshold is one Premier League suspension trigger: reaching count
// yellow cards before gameweek beforeGW (nil = any time this season) incurs
// a ban of length matches.
type yellowThreshold struct {
	count    int
	beforeGW *int
	banLen   int
}

var yellowThresholds = []yellowThreshold{
	{5, ptr(19), 1},
	{10, ptr(32), 2},
	{15, nil, 3},
}

type SuspensionRisk struct {
	YellowCards   int     `json:"yellow_cards"`
	RedCards      int     `json:"red_cards"`
	NextThreshold *int    `json:"next_threshold"`
	CardsUntilBan *int    `json:"cards_until_ban"`
	RiskLevel     string  `json:"risk_level"`
	Note          *string `json:"note"`
}

// suspensionRisk finds the next card threshold this player could reach and
// grades how close they are to it. Only the *next* threshold matters — a
// player already at 12 yellows is judged against the 15-card, 3-match band,
// not the 10-card band they've already passed.
func suspensionRisk(yellowCards, redCards, nextGW int) SuspensionRisk {
	var nextThreshold *int
	var banLen int
	for _, th := range yellowThresholds {
		if yellowCards < th.count && (th.beforeGW == nil || nextGW < *th.beforeGW) {
			nextThreshold = ptr(th.count)
			banLen = th.banLen
			break
		}
	}

	if nextThreshold == nil {
		return SuspensionRisk{YellowCards: yellowCards, RedCards: redCards, RiskLevel: "low"}
	}

	cardsUntil := *nextThreshold - yellowCards
	riskLevel := "low"
	switch {
	case cardsUntil <= 1:
		riskLevel = "high"
	case cardsUntil <= 2:
		riskLevel = "medium"
	}

	var noteParts []string
	if cardsUntil <= 2 {
		plural := "s"
		if cardsUntil == 1 {
			plural = ""
		}
		noteParts = append(noteParts, fmt.Sprintf("%d yellow card%s away from %d-match ban", cardsUntil, plural, banLen))
	}
	if redCards > 0 {
		plural := "s"
		if redCards == 1 {
			plural = ""
		}
		noteParts = append(noteParts, fmt.Sprintf("%d red card%s this season", redCards, plural))
	}

	var note *string
	if len(noteParts) > 0 {
		s := strings.Join(noteParts, ". ")
		note = &s
	}

	return SuspensionRisk{
		YellowCards: yellowCards, RedCards: redCards,
		NextThreshold: nextThreshold, CardsUntilBan: ptr(cardsUntil),
		RiskLevel: riskLevel, Note: note,
	}
}

// ordinal renders 1/2/3/n as "1st"/"2nd"/"3rd"/"nth" — good enough for set
// piece order, which never realistically goes past 3rd choice.
func ordinal(n int) string {
	switch n {
	case 1:
		return "1st"
	case 2:
		return "2nd"
	case 3:
		return "3rd"
	default:
		return fmt.Sprintf("%dth", n)
	}
}

type SquadScoutResult struct {
	Error string `json:"error,omitempty"`

	TeamID                int            `json:"team_id,omitempty"`
	Gameweek              int            `json:"gameweek,omitempty"`
	NextGameweek          int            `json:"next_gameweek,omitempty"`
	SquadSize             int            `json:"squad_size,omitempty"`
	SquadReport           []ScoutPlayer  `json:"squad_report,omitempty"`
	NumSuspensionWarnings int            `json:"num_suspension_warnings,omitempty"`
	SuspensionWarnings    []CardRisk     `json:"suspension_warnings,omitempty"`
	Insights              *ScoutInsights `json:"insights,omitempty"`
	Summary               string         `json:"summary,omitempty"`
}

type ScoutPlayer struct {
	Name             string         `json:"name"`
	Team             string         `json:"team"`
	Position         string         `json:"position"`
	Starter          bool           `json:"starter"`
	Slot             int            `json:"slot"`
	IsCaptain        bool           `json:"is_captain"`
	EPNext           float64        `json:"ep_next"`
	EPThis           float64        `json:"ep_this"`
	ScoutRisks       []string       `json:"scout_risks,omitempty"`
	SetPieces        SetPieceInfo   `json:"set_pieces"`
	SuspensionRisk   SuspensionRisk `json:"suspension_risk"`
	ICT              ICTBreakdown   `json:"ict"`
	ValueSeason      float64        `json:"value_season"`
	ValueForm        float64        `json:"value_form"`
	DreamteamCount   int            `json:"dreamteam_count"`
	Cost             float64        `json:"cost"`
	TotalPoints      int            `json:"total_points"`
	PointsPerMillion float64        `json:"points_per_million"`
	CleanSheets      *int           `json:"clean_sheets,omitempty"`
	CleanSheetsPer90 *float64       `json:"clean_sheets_per_90,omitempty"`
	XGCPer90         *float64       `json:"xGC_per_90,omitempty"`
	BPS              int            `json:"bps"`
	News             *PlayerNews    `json:"news,omitempty"`
	NewsRisk         bool           `json:"news_risk,omitempty"`
}

type SetPieceInfo struct {
	Corners         *int    `json:"corners"`
	DirectFreeKicks *int    `json:"direct_free_kicks"`
	Penalties       *int    `json:"penalties"`
	IsSetPieceTaker bool    `json:"is_set_piece_taker"`
	Summary         *string `json:"summary"`
}

type ICTBreakdown struct {
	Influence      float64 `json:"influence"`
	Creativity     float64 `json:"creativity"`
	Threat         float64 `json:"threat"`
	InfluenceRank  int     `json:"influence_rank"`
	CreativityRank int     `json:"creativity_rank"`
	ThreatRank     int     `json:"threat_rank"`
}

type CardRisk struct {
	Name          string  `json:"name"`
	Team          string  `json:"team"`
	YellowCards   int     `json:"yellow_cards"`
	RedCards      int     `json:"red_cards"`
	NextThreshold *int    `json:"next_threshold"`
	CardsUntilBan *int    `json:"cards_until_ban"`
	Note          *string `json:"note"`
}

type BlankWarning struct {
	Name     string `json:"name"`
	Team     string `json:"team"`
	Note     string `json:"note"`
	Gameweek *int   `json:"gameweek"`
}

type EPCaptainSuggestion struct {
	CurrentCaptain     string  `json:"current_captain"`
	CurrentCaptainEP   float64 `json:"current_captain_ep"`
	SuggestedCaptain   string  `json:"suggested_captain"`
	SuggestedCaptainEP float64 `json:"suggested_captain_ep"`
	EPDifference       float64 `json:"ep_difference"`
}

type EPRanking struct {
	Name    string  `json:"name"`
	Team    string  `json:"team"`
	EPNext  float64 `json:"ep_next"`
	Starter bool    `json:"starter"`
}

type SetPieceTaker struct {
	Name            string   `json:"name"`
	Team            string   `json:"team"`
	Duties          []string `json:"duties"`
	Corners         *int     `json:"corners"`
	DirectFreeKicks *int     `json:"direct_free_kicks"`
	Penalties       *int     `json:"penalties"`
	Starter         bool     `json:"starter"`
}

type ExternalSetPieceTarget struct {
	Name      string   `json:"name"`
	Team      string   `json:"team"`
	Position  string   `json:"position"`
	Cost      float64  `json:"cost"`
	EPNext    float64  `json:"ep_next"`
	Duties    []string `json:"duties"`
	Ownership float64  `json:"ownership"`
}

type ScoutInsights struct {
	BlankGWWarnings             []BlankWarning           `json:"blank_gw_warnings"`
	EPCaptainSuggestion         *EPCaptainSuggestion     `json:"ep_captain_suggestion"`
	EPRankings                  []EPRanking              `json:"ep_rankings"`
	SetPieceTakersInSquad       []SetPieceTaker          `json:"set_piece_takers_in_squad"`
	SetPieceTargetsOutsideSquad []ExternalSetPieceTarget `json:"set_piece_targets_outside_squad"`
	YellowCardRisks             []CardRisk               `json:"yellow_card_risks"`
}

// SquadScout surfaces risk flags and set-piece duty notes for a manager's
// current squad.
func (e *Engine) SquadScout(ctx context.Context, teamID int) (*SquadScoutResult, error) {
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	currentGW := bootstrap.CurrentGameweek()
	nextGW := bootstrap.NextGameweek()

	picks, err := e.client.TeamPicks(ctx, teamID, currentGW)
	if err != nil {
		return &SquadScoutResult{
			Error: fmt.Sprintf("Could not fetch picks for team %d. Check the team ID is correct.", teamID),
		}, nil
	}

	playersByID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		playersByID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)

	type epEntry struct {
		ep   float64
		info *ScoutPlayer
	}

	var squadReport []ScoutPlayer
	var blankWarnings []BlankWarning
	var setPieceTakers []SetPieceTaker
	var yellowRisks []CardRisk
	var bestEP []epEntry

	for _, pick := range picks.Picks {
		p := playersByID[pick.Element]
		if p == nil {
			continue
		}
		team := shortName(teams[p.Team])
		epNext := p.EPNext.Float()
		epThis := p.EPThis.Float()
		isStarter := pick.Position <= 11

		info := ScoutPlayer{
			Name: p.WebName, Team: team, Position: Position(p.ElementType),
			Starter: isStarter, Slot: pick.Position, IsCaptain: pick.IsCaptain,
			EPNext: epNext, EPThis: epThis,
		}

		if len(p.ScoutRisks) > 0 {
			notes := make([]string, len(p.ScoutRisks))
			for i, r := range p.ScoutRisks {
				notes[i] = r.Notes
			}
			info.ScoutRisks = notes
			if isStarter {
				for _, r := range p.ScoutRisks {
					if strings.Contains(strings.ToLower(r.Property), "blank") || strings.Contains(strings.ToLower(r.Notes), "no") {
						blankWarnings = append(blankWarnings, BlankWarning{
							Name: p.WebName, Team: team, Note: r.Notes, Gameweek: r.Gameweek,
						})
					}
				}
			}
		}

		corners, fk, pens := p.CornersAndIndirectFreekicksOrder, p.DirectFreekicksOrder, p.PenaltiesOrder
		isSetPieceTaker := corners != nil || fk != nil || pens != nil

		var spParts []string
		if corners != nil {
			spParts = append(spParts, "Corners ("+ordinal(*corners)+")")
		}
		if fk != nil {
			spParts = append(spParts, "Direct FKs ("+ordinal(*fk)+")")
		}
		if pens != nil {
			spParts = append(spParts, "Penalties ("+ordinal(*pens)+")")
		}
		var summary *string
		if len(spParts) > 0 {
			s := strings.Join(spParts, ", ")
			summary = &s
		}
		info.SetPieces = SetPieceInfo{
			Corners: corners, DirectFreeKicks: fk, Penalties: pens,
			IsSetPieceTaker: isSetPieceTaker, Summary: summary,
		}

		if isSetPieceTaker {
			// Duties here is spParts itself, not a fresh plain-text list: it's
			// set as "duties": spParts — the
			// same ordinal-formatted strings used in the summary field, in
			// corners/FK/penalties order. This differs from
			// externalSetPiece's duties below, which really is a separate,
			// plain-text, penalties/corners/FK-ordered list. Two different
			// formats for "duties" in two different output lists — a
			// mismatch worth double-checking rather than assuming, and it's
			// exactly what the golden file caught here.
			setPieceTakers = append(setPieceTakers, SetPieceTaker{
				Name: p.WebName, Team: team, Duties: spParts,
				Corners: corners, DirectFreeKicks: fk, Penalties: pens, Starter: isStarter,
			})
		}

		susp := suspensionRisk(p.YellowCards, p.RedCards, nextGW)
		info.SuspensionRisk = susp
		if susp.RiskLevel == "high" {
			yellowRisks = append(yellowRisks, CardRisk{
				Name: p.WebName, Team: team, YellowCards: susp.YellowCards, RedCards: susp.RedCards,
				NextThreshold: susp.NextThreshold, CardsUntilBan: susp.CardsUntilBan, Note: susp.Note,
			})
		}

		info.ICT = ICTBreakdown{
			Influence: p.Influence.Float(), Creativity: p.Creativity.Float(), Threat: p.Threat.Float(),
			InfluenceRank: p.InfluenceRank, CreativityRank: p.CreativityRank, ThreatRank: p.ThreatRank,
		}

		info.ValueSeason = p.ValueSeason.Float()
		info.ValueForm = p.ValueForm.Float()
		info.DreamteamCount = p.DreamteamCount
		info.Cost = float64(p.NowCost) / 10
		info.TotalPoints = p.TotalPoints
		if p.NowCost > 0 {
			info.PointsPerMillion = Round(float64(p.TotalPoints)/(float64(p.NowCost)/10), 1)
		}

		if p.ElementType == 1 || p.ElementType == 2 { // GKP or DEF
			cs := p.CleanSheets
			csPer90 := p.CleanSheetsPer90.Float()
			xgc := p.ExpectedGoalsConcededPer90.Float()
			info.CleanSheets = &cs
			info.CleanSheetsPer90 = &csPer90
			info.XGCPer90 = &xgc
		}

		info.BPS = p.BPS

		if news := GetPlayerNews(p, e.Now()); news != nil {
			info.News = news
			if HasNegativeNews(p) && isStarter {
				info.NewsRisk = true
			}
		}

		bestEP = append(bestEP, epEntry{epNext, &info})
		squadReport = append(squadReport, info)
	}

	// Stable, EP-descending: the basis for both the captain suggestion and
	// the top-5 EP ranking below.
	slices.SortStableFunc(bestEP, func(a, b epEntry) int {
		switch {
		case a.ep > b.ep:
			return -1
		case a.ep < b.ep:
			return 1
		default:
			return 0
		}
	})

	var currentCaptain *ScoutPlayer
	for i := range squadReport {
		if squadReport[i].IsCaptain {
			currentCaptain = &squadReport[i]
			break
		}
	}

	var epSuggestion *EPCaptainSuggestion
	if len(bestEP) > 0 && currentCaptain != nil {
		best := bestEP[0]
		if best.info.Name != currentCaptain.Name && best.ep > currentCaptain.EPNext {
			epSuggestion = &EPCaptainSuggestion{
				CurrentCaptain: currentCaptain.Name, CurrentCaptainEP: currentCaptain.EPNext,
				SuggestedCaptain: best.info.Name, SuggestedCaptainEP: best.ep,
				EPDifference: Round(best.ep-currentCaptain.EPNext, 1),
			}
		}
	}

	squadIDs := make(map[int]bool, len(picks.Picks))
	for _, pick := range picks.Picks {
		squadIDs[pick.Element] = true
	}

	var externalSetPiece []ExternalSetPieceTarget
	for i := range bootstrap.Elements {
		p := &bootstrap.Elements[i]
		if squadIDs[p.ID] || InjuryStatuses[p.Status] {
			continue
		}
		pens, corners, fk := p.PenaltiesOrder, p.CornersAndIndirectFreekicksOrder, p.DirectFreekicksOrder
		primaryPen := pens != nil && *pens == 1
		primaryBoth := corners != nil && *corners == 1 && fk != nil && *fk == 1
		if !primaryPen && !primaryBoth {
			continue
		}
		var duties []string
		if primaryPen {
			duties = append(duties, "penalties")
		}
		if corners != nil && *corners == 1 {
			duties = append(duties, "corners")
		}
		if fk != nil && *fk == 1 {
			duties = append(duties, "direct free kicks")
		}
		ep := p.EPNext.Float()
		if ep < 3.0 {
			continue
		}
		externalSetPiece = append(externalSetPiece, ExternalSetPieceTarget{
			Name: p.WebName, Team: shortName(teams[p.Team]), Position: Position(p.ElementType),
			Cost: float64(p.NowCost) / 10, EPNext: ep, Duties: duties,
			Ownership: p.SelectedByPercent.Float(),
		})
	}
	slices.SortStableFunc(externalSetPiece, func(a, b ExternalSetPieceTarget) int {
		switch {
		case a.EPNext > b.EPNext:
			return -1
		case a.EPNext < b.EPNext:
			return 1
		default:
			return 0
		}
	})

	epRankings := make([]EPRanking, 0, 5)
	for i := 0; i < len(bestEP) && i < 5; i++ {
		epRankings = append(epRankings, EPRanking{
			Name: bestEP[i].info.Name, Team: bestEP[i].info.Team,
			EPNext: bestEP[i].ep, Starter: bestEP[i].info.Starter,
		})
	}

	if blankWarnings == nil {
		blankWarnings = []BlankWarning{}
	}
	if setPieceTakers == nil {
		setPieceTakers = []SetPieceTaker{}
	}
	if yellowRisks == nil {
		yellowRisks = []CardRisk{}
	}
	extTop5 := externalSetPiece
	if len(extTop5) > 5 {
		extTop5 = extTop5[:5]
	}
	if extTop5 == nil {
		extTop5 = []ExternalSetPieceTarget{}
	}

	return &SquadScoutResult{
		TeamID: teamID, Gameweek: currentGW, NextGameweek: nextGW,
		SquadSize: len(squadReport), SquadReport: squadReport,
		NumSuspensionWarnings: len(yellowRisks), SuspensionWarnings: yellowRisks,
		Insights: &ScoutInsights{
			BlankGWWarnings: blankWarnings, EPCaptainSuggestion: epSuggestion,
			EPRankings: epRankings, SetPieceTakersInSquad: setPieceTakers,
			SetPieceTargetsOutsideSquad: extTop5, YellowCardRisks: yellowRisks,
		},
		Summary: buildScoutSummary(blankWarnings, epSuggestion, yellowRisks),
	}, nil
}

func buildScoutSummary(blanks []BlankWarning, epCaptain *EPCaptainSuggestion, yellows []CardRisk) string {
	var parts []string

	if len(blanks) > 0 {
		names := make([]string, len(blanks))
		for i, b := range blanks {
			names[i] = b.Name
		}
		parts = append(parts, fmt.Sprintf("Blank GW alert: %s have no fixture", strings.Join(names, ", ")))
	}

	if epCaptain != nil {
		parts = append(parts, fmt.Sprintf(
			"FPL's data suggests %s (EP %s) over %s (EP %s) as captain",
			epCaptain.SuggestedCaptain, FloatStr(epCaptain.SuggestedCaptainEP),
			epCaptain.CurrentCaptain, FloatStr(epCaptain.CurrentCaptainEP),
		))
	}

	if len(yellows) > 0 {
		names := make([]string, len(yellows))
		for i, y := range yellows {
			cardsUntil := 0
			if y.CardsUntilBan != nil {
				cardsUntil = *y.CardsUntilBan
			}
			names[i] = fmt.Sprintf("%s (%d YC, %d from ban)", y.Name, y.YellowCards, cardsUntil)
		}
		parts = append(parts, fmt.Sprintf("Suspension risk: %s", strings.Join(names, ", ")))
	}

	if len(parts) == 0 {
		// The trailing period here is deliberate, not a stray edit: this
		// exact string already ends with "." and then the caller
		// unconditionally appends another below, producing a genuine double
		// period ("...healthy.."). Existing behaviour, reproduced exactly —
		// see the Phase 7 redesign note in the plan for output defects like
		// this one, which get fixed there with backtest-style scrutiny,
		// rather than silently changed here.
		parts = append(parts, "No major risks detected. Squad looks healthy.")
	}

	return strings.Join(parts, ". ") + "."
}
