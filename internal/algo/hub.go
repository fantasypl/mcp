package algo

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/fantasypl/mcp/internal/fpl"
)

// ManagerHubResult is the aggregate report fpl_manager_hub returns: manager
// status, squad overview and health, and the output of every other
// team-scoped algorithm run against the same gameweek, so a client gets one
// coherent picture instead of six separate calls that could observe
// different cache states.
type ManagerHubResult struct {
	TeamID                int                  `json:"team_id"`
	Gameweek              int                  `json:"gameweek"`
	PreppingFor           string               `json:"prepping_for"`
	ManagerStatus         *fpl.ManagerStatus   `json:"manager_status"`
	SquadValue            float64              `json:"squad_value"`
	Bank                  float64              `json:"bank"`
	TotalBudget           float64              `json:"total_budget"`
	SeasonSummary         HubSeasonSummary     `json:"season_summary"`
	SquadSize             int                  `json:"squad_size"`
	SquadValid            bool                 `json:"squad_valid"`
	NumStarters           int                  `json:"num_starters"`
	NumBench              int                  `json:"num_bench"`
	Squad                 []HubSquadEntry      `json:"squad"`
	SquadHealth           HubSquadHealth       `json:"squad_health"`
	CaptainRecommendation []CaptainPick        `json:"captain_recommendation"`
	TransferSuggestions   []TransferSuggestion `json:"transfer_suggestions"`
	DifferentialTargets   []Differential       `json:"differential_targets"`
	FixtureOutlook        HubFixtureOutlook    `json:"fixture_outlook"`
	PriceDropRisks        []HubPriceRisk       `json:"price_drop_risks"`
	PricePredictions      HubPricePredictions  `json:"price_predictions"`
	PoweredBy             string               `json:"powered_by"`
}

type HubSeasonSummary struct {
	TotalPoints     int           `json:"total_points"`
	GameweeksPlayed int           `json:"gameweeks_played"`
	AvgPointsPerGW  float64       `json:"avg_points_per_gw"`
	BestGameweek    *HubGWResult  `json:"best_gameweek"`
	WorstGameweek   *HubGWResult  `json:"worst_gameweek"`
	ChipsUsed       []HubChipUsed `json:"chips_used"`
	ChipsRemaining  []string      `json:"chips_remaining"`
	HalfSeason      string        `json:"half_season"`
}

type HubGWResult struct {
	GW     int `json:"gw"`
	Points int `json:"points"`
}

type HubChipUsed struct {
	Chip     string  `json:"chip"`
	Gameweek int     `json:"gameweek"`
	Note     *string `json:"note,omitempty"`
}

type HubSquadEntry struct {
	Slot          int      `json:"slot"`
	Starter       bool     `json:"starter"`
	ElementID     int      `json:"element_id"`
	Name          string   `json:"name"`
	Team          string   `json:"team"`
	TeamFullName  string   `json:"team_full_name"`
	Position      string   `json:"position"`
	Cost          float64  `json:"cost"`
	Form          float64  `json:"form"`
	PointsPerGame float64  `json:"points_per_game"`
	TotalPoints   int      `json:"total_points"`
	ICTIndex      float64  `json:"ict_index"`
	IsCaptain     bool     `json:"is_captain"`
	IsViceCaptain bool     `json:"is_vice_captain"`
	Opponent      string   `json:"opponent"`
	Venue         string   `json:"venue"`
	FDR           *float64 `json:"fdr"`
	CaptainScore  float64  `json:"captain_score"`
	Status        string   `json:"status"`
	Minutes       int      `json:"minutes"`
	SelectedByPct float64  `json:"selected_by_pct"`
}

type HubSquadHealth struct {
	InjuredOrDoubtful   []HubInjuredEntry      `json:"injured_or_doubtful"`
	PoorFormStarters    []HubPoorFormEntry     `json:"poor_form_starters"`
	ToughFixturesThisGW []HubToughFixtureEntry `json:"tough_fixtures_this_gw"`
}

type HubInjuredEntry struct {
	Name      string `json:"name"`
	ElementID int    `json:"element_id"`
	Status    string `json:"status"`
}

type HubPoorFormEntry struct {
	Name      string  `json:"name"`
	ElementID int     `json:"element_id"`
	Form      float64 `json:"form"`
}

type HubToughFixtureEntry struct {
	Name      string   `json:"name"`
	ElementID int      `json:"element_id"`
	Opponent  string   `json:"opponent"`
	FDR       *float64 `json:"fdr"`
}

type HubPriceRisk struct {
	Name         string `json:"name"`
	ElementID    int    `json:"element_id"`
	NetTransfers int    `json:"net_transfers"`
	Risk         string `json:"risk"`
}

type HubFixtureOutlook struct {
	TeamsByDifficulty []TeamOutlook  `json:"teams_by_difficulty"`
	PlayersToTarget   []TargetPlayer `json:"players_to_target"`
}

type HubPricePredictions struct {
	LikelyRisers  []PriceMove `json:"likely_risers"`
	LikelyFallers []PriceMove `json:"likely_fallers"`
}

// ManagerHub runs a manager's full intelligence report: auto-detected
// status (bank, free transfers, chips) drives transfer suggestions, and
// every other team-scoped algorithm runs in parallel against the same
// gameweek's bootstrap and fixtures.
//
// Three sequential stages, each internally parallel — mirroring
// mcp_server._fpl_manager_hub_impl's three asyncio.gather calls, since the
// second stage needs current_gw from the first and the third needs
// free_transfers/bank from the second:
//
//  1. bootstrap + fixtures
//  2. manager status + this gameweek's picks + season history
//  3. captain picks, transfer suggestions, differentials, fixture outlook,
//     price predictions
func (e *Engine) ManagerHub(ctx context.Context, teamID int, gameweeksAhead int) (*ManagerHubResult, error) {
	var bootstrap *fpl.Bootstrap
	var fixtures []fpl.Fixture
	{
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() (err error) { bootstrap, err = e.client.Bootstrap(gctx); return })
		g.Go(func() (err error) { fixtures, err = e.client.Fixtures(gctx); return })
		if err := g.Wait(); err != nil {
			return nil, err
		}
	}
	currentGW := bootstrap.CurrentGameweek()
	nextGW := bootstrap.NextGameweek()

	var mgrStatus *fpl.ManagerStatus
	var picks *fpl.TeamPicks
	var history *fpl.TeamHistory
	{
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() (err error) { mgrStatus, err = e.client.ManagerStatus(gctx, teamID, bootstrap); return })
		g.Go(func() (err error) { picks, err = e.client.TeamPicks(gctx, teamID, currentGW); return })
		g.Go(func() (err error) { history, err = e.client.TeamHistory(gctx, teamID); return })
		if err := g.Wait(); err != nil {
			return nil, err
		}
	}

	var captainResult *CaptainResult
	var transferResult any
	var diffResult *DifferentialResult
	var fixtureResult *FixtureOutlookResult
	var priceResult *PriceResult
	{
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() (err error) { captainResult, err = e.CaptainPicks(gctx, &nextGW, 5); return })
		g.Go(func() (err error) {
			transferResult, err = e.TransferSuggestions(gctx, teamID, mgrStatus.FreeTransfers, mgrStatus.Bank)
			return
		})
		g.Go(func() (err error) { diffResult, err = e.Differentials(gctx, 10, &nextGW, 10); return })
		g.Go(func() (err error) { fixtureResult, err = e.FixtureOutlook(gctx, gameweeksAhead, ""); return })
		g.Go(func() (err error) { priceResult, err = e.PricePredictions(gctx, 0); return })
		if err := g.Wait(); err != nil {
			return nil, err
		}
	}

	// Real squad value from history, not summed now_cost: FPL's history
	// "value" is total team value (squad + bank), and now_cost inflates
	// relative to what the squad would actually sell for.
	var squadValue, bank float64
	season := history.Current
	if len(season) > 0 {
		latest := season[len(season)-1]
		apiTotalValue := float64(latest.Value) / 10
		bank = float64(latest.Bank) / 10
		squadValue = Round(apiTotalValue-bank, 1)
	}

	playersByID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		playersByID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)
	fixtureMap := buildFixtureMap(fixtures, nextGW, teams)

	squad := make([]HubSquadEntry, 0, len(picks.Picks))
	for _, pick := range picks.Picks {
		p := playersByID[pick.Element]
		if p == nil {
			continue
		}
		team := teams[p.Team]
		playerFixtures := fixtureMap[p.Team]
		captainScore := e.scorePlayer(p, playerFixtures)

		var opponentStr, venue string
		var fdr *float64
		if len(playerFixtures) > 0 {
			parts := make([]string, 0, len(playerFixtures))
			for _, f := range playerFixtures {
				v := "A"
				if f.IsHome {
					v = "H"
				}
				parts = append(parts, fmt.Sprintf("%s(%s)", shortName(teams[f.Opponent]), v))
			}
			opponentStr = strings.Join(parts, ", ")
			if playerFixtures[0].IsHome {
				venue = "Home"
			} else {
				venue = "Away"
			}
			first := playerFixtures[0].FDR
			fdr = &first
		} else {
			opponentStr = "?"
			venue = "?"
		}

		status := p.Status
		if status == "" {
			status = "a"
		}
		squad = append(squad, HubSquadEntry{
			Slot: pick.Position, Starter: pick.Position <= 11, ElementID: p.ID,
			Name: p.WebName, Team: shortName(team), TeamFullName: fullName(team),
			Position: Position(p.ElementType), Cost: float64(p.NowCost) / 10,
			Form: p.Form.Float(), PointsPerGame: p.PointsPerGame.Float(),
			TotalPoints: p.TotalPoints, ICTIndex: p.ICTIndex.Float(),
			IsCaptain: pick.IsCaptain, IsViceCaptain: pick.IsViceCaptain,
			Opponent: opponentStr, Venue: venue, FDR: fdr, CaptainScore: captainScore,
			Status: status, Minutes: p.Minutes, SelectedByPct: p.SelectedByPercent.Float(),
		})
	}

	numStarters := 0
	for _, s := range squad {
		if s.Starter {
			numStarters++
		}
	}

	injured := make([]HubInjuredEntry, 0)
	poorForm := make([]HubPoorFormEntry, 0)
	toughFixtures := make([]HubToughFixtureEntry, 0)
	priceRisks := make([]HubPriceRisk, 0)
	for _, s := range squad {
		if InjuryStatuses[s.Status] {
			injured = append(injured, HubInjuredEntry{Name: s.Name, ElementID: s.ElementID, Status: s.Status})
		}
		if s.Starter && s.Form <= 2.0 {
			poorForm = append(poorForm, HubPoorFormEntry{Name: s.Name, ElementID: s.ElementID, Form: s.Form})
		}
		if s.Starter && s.FDR != nil && *s.FDR >= 4 {
			toughFixtures = append(toughFixtures, HubToughFixtureEntry{Name: s.Name, ElementID: s.ElementID, Opponent: s.Opponent, FDR: s.FDR})
		}
		if p := playersByID[s.ElementID]; p != nil {
			net := p.TransfersInEvent - p.TransfersOutEvent
			if net < -50_000 {
				priceRisks = append(priceRisks, HubPriceRisk{Name: s.Name, ElementID: s.ElementID, NetTransfers: net, Risk: "Likely to fall"})
			}
		}
	}

	totalPoints := 0
	for _, gw := range season {
		totalPoints += gw.Points
	}
	var bestGW, worstGW *HubGWResult
	if len(season) > 0 {
		best, worst := season[0], season[0]
		for _, gw := range season[1:] {
			if gw.Points > best.Points {
				best = gw
			}
			if gw.Points < worst.Points {
				worst = gw
			}
		}
		bestGW = &HubGWResult{GW: best.Event, Points: best.Points}
		worstGW = &HubGWResult{GW: worst.Event, Points: worst.Points}
	}
	avgPointsPerGW := 0.0
	if len(season) > 0 {
		avgPointsPerGW = Round(float64(totalPoints)/float64(len(season)), 1)
	}
	halfSeason := "first"
	if currentGW > halfwayGW {
		halfSeason = "second"
	}
	chipsUsed := make([]HubChipUsed, 0, len(history.Chips))
	for _, ch := range history.Chips {
		entry := HubChipUsed{Chip: ch.Name, Gameweek: ch.Event}
		if currentGW > halfwayGW && ch.Event <= halfwayGW {
			note := "first half — has reset"
			entry.Note = &note
		}
		chipsUsed = append(chipsUsed, entry)
	}

	var transferSuggestions []TransferSuggestion
	if tr, ok := transferResult.(*TransferSuggestionsResult); ok {
		transferSuggestions = tr.TransferSuggestions
	}
	if transferSuggestions == nil {
		transferSuggestions = []TransferSuggestion{}
	}

	diffTargets := diffResult.Differentials
	if len(diffTargets) > 10 {
		diffTargets = diffTargets[:10]
	}
	teamsByDifficulty := fixtureResult.TeamsByDifficulty
	if len(teamsByDifficulty) > 10 {
		teamsByDifficulty = teamsByDifficulty[:10]
	}
	likelyRisers := priceResult.LikelyRisers
	if len(likelyRisers) > 5 {
		likelyRisers = likelyRisers[:5]
	}
	likelyFallers := priceResult.LikelyFallers
	if len(likelyFallers) > 5 {
		likelyFallers = likelyFallers[:5]
	}

	return &ManagerHubResult{
		TeamID: teamID, Gameweek: currentGW, PreppingFor: fmt.Sprintf("GW%d", nextGW),
		ManagerStatus: mgrStatus, SquadValue: squadValue, Bank: bank, TotalBudget: Round(squadValue+bank, 1),
		SeasonSummary: HubSeasonSummary{
			TotalPoints: totalPoints, GameweeksPlayed: len(season), AvgPointsPerGW: avgPointsPerGW,
			BestGameweek: bestGW, WorstGameweek: worstGW, ChipsUsed: chipsUsed,
			ChipsRemaining: mgrStatus.ChipsRemaining, HalfSeason: halfSeason,
		},
		SquadSize: len(squad), SquadValid: numStarters == 11, NumStarters: numStarters, NumBench: len(squad) - numStarters,
		Squad: squad,
		SquadHealth: HubSquadHealth{
			InjuredOrDoubtful: injured, PoorFormStarters: poorForm, ToughFixturesThisGW: toughFixtures,
		},
		CaptainRecommendation: captainResult.Picks,
		TransferSuggestions:   transferSuggestions,
		DifferentialTargets:   diffTargets,
		FixtureOutlook:        HubFixtureOutlook{TeamsByDifficulty: teamsByDifficulty, PlayersToTarget: fixtureResult.PlayersToTarget},
		PriceDropRisks:        priceRisks,
		PricePredictions:      HubPricePredictions{LikelyRisers: likelyRisers, LikelyFallers: likelyFallers},
		PoweredBy:             "FPL Intelligence — github.com/fantasypl/mcp",
	}, nil
}
