package algo

import (
	"context"
	"fmt"
	"slices"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

// Transfer suggestions rank a manager's own squad worst-to-best by transfer
// value, then find affordable, better-value replacements for the weakest
// picks — the free-transfer count sets how many sell candidates to surface.
//
// This is the first of six algorithms that need a manager's actual squad via
// GET /entry/{id}/event/{gw}/picks/. That endpoint cannot be sourced for any
// past season — verified live against the real API: gameweek numbers are
// reused every season with no season parameter
// anywhere, so once a season rolls over its picks are gone for good, from
// every source. Tests here run against a synthetic but schema-valid squad
// (testdata/picks_squad1.json, built by scripts/make_squad_fixture.py from
// real player IDs) rather than a captured real one.

// TransferError is what a failed team lookup returns — the transfer algorithm
// itself produces this shape (not a raised exception) when a team_id can't be
// resolved, so it is modelled as a distinct result shape here too rather than
// as a Go error. See TransferSuggestions.
type TransferError struct {
	Error string `json:"error"`
}

type TransferSuggestionsResult struct {
	TeamID              int                  `json:"team_id"`
	Gameweek            int                  `json:"gameweek"`
	FreeTransfers       int                  `json:"free_transfers"`
	BankBalanceM        float64              `json:"bank_balance_m"`
	BudgetNote          string               `json:"budget_note"`
	NumSuggestions      int                  `json:"num_suggestions"`
	TransferSuggestions []TransferSuggestion `json:"transfer_suggestions"`
	SquadSize           int                  `json:"squad_size"`
	SquadOverview       []SquadOverviewEntry `json:"squad_overview"`
}

type TransferSuggestion struct {
	TransferOut       TransferOutPlayer  `json:"transfer_out"`
	TransferInOptions []TransferInOption `json:"transfer_in_options"`
	BudgetAvailable   float64            `json:"budget_available"`
}

type TransferOutPlayer struct {
	ID         int     `json:"id"`
	Name       string  `json:"name"`
	Team       string  `json:"team"`
	Position   string  `json:"position"`
	Cost       float64 `json:"cost"`
	Form       float64 `json:"form"`
	ValueScore float64 `json:"value_score"`
	Reasoning  string  `json:"reasoning"`
}

type TransferInOption struct {
	ID           int          `json:"id"`
	Name         string       `json:"name"`
	Team         string       `json:"team"`
	TeamFullName string       `json:"team_full_name"`
	Position     string       `json:"position"`
	Cost         float64      `json:"cost"`
	Form         float64      `json:"form"`
	PPG          float64      `json:"ppg"`
	ValueScore   float64      `json:"value_score"`
	CaptainScore float64      `json:"captain_score"`
	Fixture      *FixtureLite `json:"fixture"`
}

// FixtureLite is a single fixture's raw fields, as opposed to FixtureInfo's
// richer shape — this is what the "first fixture for backward compat" field
// actually stores: fdr and venue on the fixture map's own team-ID terms, not
// resolved to short names.
type FixtureLite struct {
	FDR      float64 `json:"fdr"`
	IsHome   bool    `json:"is_home"`
	Opponent int     `json:"opponent"`
}

type SquadOverviewEntry struct {
	Name       string  `json:"name"`
	Team       string  `json:"team"`
	Position   string  `json:"position"`
	Form       float64 `json:"form"`
	ValueScore float64 `json:"value_score"`
}

// squadEntry is the internal working shape for one of the manager's 15
// players — including keeping the source
// fpl.Player around for reasoning that needs the full record (news, status).
type squadEntry struct {
	player     *fpl.Player
	team       string
	position   string
	cost       float64
	form       float64
	ppg        float64
	status     string
	valueScore float64
	fixture    *TeamFixture // first fixture this GW, or nil if blanking
	fixtures   []TeamFixture
}

// playerValueScore scores recent output plus a short fixture outlook,
// penalised for injury risk. future is up to two
// gameweeks of fixtures beyond the immediate one — GW+1 weighted 0.5, GW+2
// weighted 0.3 — so a transfer is judged as a medium-term decision rather
// than chasing a single good week.
func playerValueScore(p *fpl.Player, fixtures []TeamFixture, future [][]TeamFixture) float64 {
	form := p.Form.Float()
	ppg := p.PointsPerGame.Float()

	fixtureScore := -3.0
	if len(fixtures) > 0 {
		fixtureScore = 0.0
		for _, f := range fixtures {
			fixtureScore += -f.FDR*1.0 + homeBonusOf(f.IsHome, 0.5)
		}
		fixtureScore += float64(max(0, len(fixtures)-1)) * 2.0
	}

	for i, gwFixtures := range future {
		weight := 0.3
		if i == 0 {
			weight = 0.5
		}
		for _, f := range gwFixtures {
			fixtureScore += (-f.FDR*1.0 + homeBonusOf(f.IsHome, 0.5)) * weight
		}
	}

	score := form*2.0 + ppg*1.0 + fixtureScore

	if p.ElementType == 2 { // DEF
		score += p.DefensiveContributionPer90.Float() * 0.5
	}
	if InjuryStatuses[p.Status] {
		score -= 5
	}
	score += NewsPenaltyScore(p)

	return Round(score, 2)
}

func homeBonusOf(isHome bool, bonus float64) float64 {
	if isHome {
		return bonus
	}
	return 0
}

func firstFixture(fixtures []TeamFixture) *TeamFixture {
	if len(fixtures) == 0 {
		return nil
	}
	return &fixtures[0]
}

// sellReason explains why this player is the weakest
// link in the squad, in the same priority order the algorithm checks — form,
// then availability, then the fixture ahead, then any concerning news.
//
// A method rather than a free function because news age is rendered as a
// relative string ("2 days ago") and must go through the engine's injected
// clock — see Engine.Now and the same reasoning in captain.buildReasoning.
func (e *Engine) sellReason(se *squadEntry) string {
	var reasons []string

	if se.form <= 2.0 {
		reasons = append(reasons, "poor form")
	}
	if se.status == "d" || se.status == "i" || se.status == "s" {
		reasons = append(reasons, "injury/suspension concern")
	}
	fdr := 3.0
	if se.fixture != nil {
		fdr = se.fixture.FDR
	}
	if fdr >= 4 {
		reasons = append(reasons, fmt.Sprintf("tough upcoming fixture (FDR %d)", TruncInt(fdr)))
	}
	if se.player != nil {
		news := FormatNewsForReasoning(se.player, e.Now())
		if news != "" && HasNegativeNews(se.player) {
			reasons = append(reasons, "news: "+news)
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "lowest squad value score")
	}

	return Capitalize(joinComma(reasons))
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// TransferSuggestions recommends players to sell and buy given a budget and
// number of free transfers.
//
// The return value is either a *TransferSuggestionsResult on success or a
// *TransferError when the team's picks can't be fetched — two distinct JSON
// shapes, returning
// {"error": ...} as a normal (non-exceptional) result rather than raising.
// The Go error return is reserved for failures unrelated to team lookup
// (bootstrap or fixtures fetch failing), which the transfer flow doesn't
// specifically handle either.
func (e *Engine) TransferSuggestions(ctx context.Context, teamID, freeTransfers int, bankM float64) (any, error) {
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	fixtures, err := e.client.Fixtures(ctx)
	if err != nil {
		return nil, err
	}
	nextGW := bootstrap.NextGameweek()

	// The first attempt catches any fetch error (network
	// failure, 404, decode error alike) and retries against the current
	// gameweek before giving up — preserved here even though it means a
	// transient fetch error is surfaced to the user as "check your team ID."
	picks, pErr := e.client.TeamPicks(ctx, teamID, nextGW)
	if pErr != nil {
		currentGW := bootstrap.CurrentGameweek()
		picks, pErr = e.client.TeamPicks(ctx, teamID, currentGW)
		if pErr != nil {
			return &TransferError{
				Error: fmt.Sprintf("Could not fetch picks for team %d. Check the team ID is correct.", teamID),
			}, nil
		}
	}

	byID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		byID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)
	fixtureMap := buildFixtureMap(fixtures, nextGW, teams)

	var futureFixtureMaps []map[int][]TeamFixture
	for _, futureGW := range []int{nextGW + 1, nextGW + 2} {
		if futureGW <= 38 {
			futureFixtureMaps = append(futureFixtureMaps, buildFixtureMap(fixtures, futureGW, teams))
		}
	}

	squad := make([]squadEntry, 0, len(picks.Picks))
	squadIDs := make(map[int]bool, len(picks.Picks))
	for _, pick := range picks.Picks {
		p := byID[pick.Element]
		if p == nil {
			continue
		}
		squadIDs[p.ID] = true

		pf := fixtureMap[p.Team]
		future := make([][]TeamFixture, len(futureFixtureMaps))
		for i, fm := range futureFixtureMaps {
			future[i] = fm[p.Team]
		}
		score := playerValueScore(p, pf, future)

		squad = append(squad, squadEntry{
			player:     p,
			team:       shortName(teams[p.Team]),
			position:   Position(p.ElementType),
			cost:       float64(p.NowCost) / 10,
			form:       p.Form.Float(),
			ppg:        p.PointsPerGame.Float(),
			status:     nonEmptyOr(p.Status, "a"),
			valueScore: score,
			fixture:    firstFixture(pf),
			fixtures:   pf,
		})
	}

	// Worst value first: these are the sell candidates.
	slices.SortStableFunc(squad, func(a, b squadEntry) int {
		switch {
		case a.valueScore < b.valueScore:
			return -1
		case a.valueScore > b.valueScore:
			return 1
		default:
			return 0
		}
	})

	numOut := min(freeTransfers, len(squad))
	sellCandidates := squad[:numOut]

	suggestions := make([]TransferSuggestion, 0, numOut)
	for _, sell := range sellCandidates {
		// FPL pays selling_price, not current price, but purchase price isn't
		// exposed by this endpoint — current price is the best available
		// estimate, as required by the transfer estimate contract.
		budget := sell.cost + bankM
		posType := sell.player.ElementType

		var replacements []TransferInOption
		for i := range bootstrap.Elements {
			p := &bootstrap.Elements[i]
			if p.ID == sell.player.ID || p.ElementType != posType {
				continue
			}
			if float64(p.NowCost)/10 > budget {
				continue
			}
			if InjuryStatuses[p.Status] {
				continue
			}
			if squadIDs[p.ID] {
				continue
			}
			pf := fixtureMap[p.Team]
			if len(pf) == 0 {
				continue // blanking this gameweek
			}
			future := make([][]TeamFixture, len(futureFixtureMaps))
			for i, fm := range futureFixtureMaps {
				future[i] = fm[p.Team]
			}
			score := playerValueScore(p, pf, future)
			if score <= sell.valueScore {
				continue
			}

			replacements = append(replacements, TransferInOption{
				ID:           p.ID,
				Name:         p.WebName,
				Team:         shortName(teams[p.Team]),
				TeamFullName: fullName(teams[p.Team]),
				Position:     Position(p.ElementType),
				Cost:         float64(p.NowCost) / 10,
				Form:         p.Form.Float(),
				PPG:          p.PointsPerGame.Float(),
				ValueScore:   score,
				CaptainScore: Round(e.scorePlayer(p, pf), 1),
				Fixture:      fixtureLiteOf(firstFixture(pf)),
			})
		}

		slices.SortStableFunc(replacements, func(a, b TransferInOption) int {
			switch {
			case a.ValueScore > b.ValueScore:
				return -1
			case a.ValueScore < b.ValueScore:
				return 1
			default:
				return 0
			}
		})
		if len(replacements) > 5 {
			replacements = replacements[:5]
		}
		if replacements == nil {
			replacements = []TransferInOption{}
		}

		suggestions = append(suggestions, TransferSuggestion{
			TransferOut: TransferOutPlayer{
				ID: sell.player.ID, Name: sell.player.WebName, Team: sell.team,
				Position: sell.position, Cost: sell.cost, Form: sell.form,
				ValueScore: sell.valueScore, Reasoning: e.sellReason(&sell),
			},
			TransferInOptions: replacements,
			BudgetAvailable:   Round(budget, 1),
		})
	}

	squadOverview := make([]SquadOverviewEntry, 0, len(squad))
	for _, s := range squad {
		squadOverview = append(squadOverview, SquadOverviewEntry{
			Name: s.player.WebName, Team: s.team, Position: s.position,
			Form: s.form, ValueScore: s.valueScore,
		})
	}

	return &TransferSuggestionsResult{
		TeamID:              teamID,
		Gameweek:            nextGW,
		FreeTransfers:       freeTransfers,
		BankBalanceM:        bankM,
		BudgetNote:          "Budget estimates use current player prices. FPL's selling price may differ if a player's value has risen since purchase — check the FPL app for your exact budget.",
		NumSuggestions:      len(suggestions),
		TransferSuggestions: suggestions,
		SquadSize:           len(squadOverview),
		SquadOverview:       squadOverview,
	}, nil
}

func fixtureLiteOf(f *TeamFixture) *FixtureLite {
	if f == nil {
		return nil
	}
	return &FixtureLite{FDR: f.FDR, IsHome: f.IsHome, Opponent: f.Opponent}
}

func nonEmptyOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
