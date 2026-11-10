package algo

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/fantasypl/mcp/internal/fpl"
)

// manager_history is a manager's season, gameweek by gameweek (issue #49):
// points before and after hits, bench points, ranks, the gameweek's average
// and highest score, the chip played, captain and vice-captain, automatic
// substitutions, and what the best captain in the XI would have scored.
//
// It reads /entry/{id}/history/ once, then each played gameweek's picks and
// live points, six gameweeks at a time. The client caches all three, so a
// repeated call within a minute costs nothing.
//
// Captain return: the captain's points, or the vice-captain's when the
// captain didn't play and the vice did, which is when FPL moves the armband.
// The best alternative is the highest scorer in the XI that actually counted
// (after automatic substitutions; all 15 under a Bench Boost). Points forgone
// are (best - captain return) x (captain multiplier - 1): 1 normally, 2
// under a Triple Captain.

// ManagerHistoryError is returned when the team's history can't be fetched.
type ManagerHistoryError struct {
	Error string `json:"error"`
}

// HistoryCaptain is a player and his points for one gameweek, before any
// captain multiplier.
type HistoryCaptain struct {
	Name   string `json:"name"`
	Points int    `json:"points"`
}

// HistoryAutoSub is one automatic substitution.
type HistoryAutoSub struct {
	In  string `json:"in"`
	Out string `json:"out"`
}

// ManagerHistoryGW is one gameweek of manager_history.
type ManagerHistoryGW struct {
	Gameweek      int    `json:"gameweek"`
	Points        int    `json:"points"`        // before hits, as FPL shows the gameweek score
	TransferCost  int    `json:"transfer_cost"` // hit cost taken this gameweek
	NetPoints     int    `json:"net_points"`
	PointsOnBench int    `json:"points_on_bench"`
	OverallRank   int    `json:"overall_rank"`
	GWRank        int    `json:"gw_rank"`
	GWAverage     int    `json:"gw_average"`
	GWHighest     int    `json:"gw_highest"`
	Chip          string `json:"chip,omitempty"`

	// The fields below need the gameweek's picks and live points, and are
	// omitted when those can't be fetched.
	Captain              *HistoryCaptain  `json:"captain,omitempty"` // the captain return; the vice when the captain didn't play
	CaptainNote          string           `json:"captain_note,omitempty"`
	ViceCaptain          string           `json:"vice_captain,omitempty"`
	AutoSubs             []HistoryAutoSub `json:"auto_subs,omitempty"`
	BestCaptain          *HistoryCaptain  `json:"best_captain,omitempty"` // best scorer in the XI that counted
	CaptainPointsForgone int              `json:"captain_points_forgone"`
}

// ManagerHistorySummary totals the season so far.
type ManagerHistorySummary struct {
	GameweeksPlayed      int      `json:"gameweeks_played"`
	GrossPoints          int      `json:"gross_points"`
	NetPoints            int      `json:"net_points"` // FPL's official total
	Hits                 int      `json:"hits"`       // -4 transfers taken
	HitCost              int      `json:"hit_cost"`
	PointsOnBench        int      `json:"points_on_bench"`
	BenchPointsPerGW     float64  `json:"bench_points_per_gw"`
	CaptainPoints        int      `json:"captain_points"`         // sum of captain returns, before the multiplier
	BestCaptainPoints    int      `json:"best_captain_points"`    // sum of the best scorer in the XI each week
	CaptainPointsForgone int      `json:"captain_points_forgone"` // with the multiplier applied
	CaptainGameweeks     int      `json:"captain_gameweeks"`      // gameweeks with picks and live data
	ChipsPlayed          []string `json:"chips_played"`
	OverallRankStart     int      `json:"overall_rank_start"`
	OverallRankNow       int      `json:"overall_rank_now"`
}

// ManagerHistoryResult is manager_history's output.
type ManagerHistoryResult struct {
	TeamID    int                   `json:"team_id"`
	Gameweeks []ManagerHistoryGW    `json:"gameweeks"`
	Summary   ManagerHistorySummary `json:"summary"`
	Method    string                `json:"method"`
}

const managerHistoryMethod = "points is the gameweek score before hits, as FPL shows it; net_points subtracts transfer_cost. captain is the captain return: the captain's points, or the vice-captain's if the captain didn't play. best_captain is the highest scorer in the XI that counted (after automatic substitutions; all 15 under a Bench Boost). captain_points_forgone = (best_captain - captain) x (captain multiplier - 1)."

// ManagerHistory builds a manager's season history.
func (e *Engine) ManagerHistory(ctx context.Context, teamID int) (any, error) {
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	history, err := e.client.TeamHistory(ctx, teamID)
	if err != nil {
		return &ManagerHistoryError{Error: fmt.Sprintf("Could not fetch history for team %d. Check the team ID is correct.", teamID)}, nil
	}

	names := make(map[int]string, len(bootstrap.Elements))
	for _, p := range bootstrap.Elements {
		names[p.ID] = p.WebName
	}
	events := make(map[int]fpl.Event, len(bootstrap.Events))
	for _, ev := range bootstrap.Events {
		events[ev.ID] = ev
	}
	chipByGW := make(map[int]string, len(history.Chips))
	for _, c := range history.Chips {
		chipByGW[c.Event] = c.Name
	}

	picks, live := e.historyGameweekData(ctx, teamID, history.Current)

	rows := make([]ManagerHistoryGW, 0, len(history.Current))
	var sum ManagerHistorySummary
	for _, g := range history.Current {
		row := ManagerHistoryGW{
			Gameweek: g.Event, Points: g.Points, TransferCost: g.EventTransfersCost,
			NetPoints: g.Points - g.EventTransfersCost, PointsOnBench: g.PointsOnBench,
			OverallRank: g.OverallRank, GWRank: g.Rank, GWAverage: events[g.Event].AverageEntryScore,
			Chip: chipByGW[g.Event],
		}
		if h := events[g.Event].HighestScore; h != nil {
			row.GWHighest = *h
		}

		if pk, ok := picks[g.Event]; ok && live[g.Event] != nil {
			r := captainReturn(pk, live[g.Event], names)
			row.Captain, row.BestCaptain = &r.effective, &r.best
			row.CaptainPointsForgone, row.CaptainNote = r.forgone, r.note
			row.ViceCaptain = names[r.viceID]
			for _, s := range pk.AutomaticSubs {
				row.AutoSubs = append(row.AutoSubs, HistoryAutoSub{In: names[s.ElementIn], Out: names[s.ElementOut]})
			}
			sum.CaptainGameweeks++
			sum.CaptainPoints += r.effective.Points
			sum.BestCaptainPoints += r.best.Points
			sum.CaptainPointsForgone += r.forgone
		}

		sum.GrossPoints += row.Points
		sum.NetPoints += row.NetPoints
		sum.HitCost += row.TransferCost
		sum.PointsOnBench += row.PointsOnBench
		if row.Chip != "" {
			sum.ChipsPlayed = append(sum.ChipsPlayed, fmt.Sprintf("%s (GW%d)", row.Chip, row.Gameweek))
		}
		rows = append(rows, row)
	}

	sum.GameweeksPlayed = len(rows)
	sum.Hits = sum.HitCost / -hitCost // hitCost is -4
	if len(rows) > 0 {
		sum.BenchPointsPerGW = Round(float64(sum.PointsOnBench)/float64(len(rows)), 1)
		sum.OverallRankStart, sum.OverallRankNow = rows[0].OverallRank, rows[len(rows)-1].OverallRank
	}
	if sum.ChipsPlayed == nil {
		sum.ChipsPlayed = []string{}
	}
	return &ManagerHistoryResult{TeamID: teamID, Gameweeks: rows, Summary: sum, Method: managerHistoryMethod}, nil
}

// historyGameweekData fetches each played gameweek's picks and live points,
// six at a time. A gameweek whose fetch fails is left out of both maps.
func (e *Engine) historyGameweekData(ctx context.Context, teamID int, gws []fpl.HistoryGameweek) (map[int]*fpl.TeamPicks, map[int]map[int]fpl.LiveStats) {
	var mu sync.Mutex
	picks := make(map[int]*fpl.TeamPicks, len(gws))
	live := make(map[int]map[int]fpl.LiveStats, len(gws))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(6)
	for _, gw := range gws {
		g.Go(func() error {
			pk, err := e.client.TeamPicks(gctx, teamID, gw.Event)
			if err != nil {
				return nil // best-effort: the row keeps its history fields
			}
			lv, err := e.client.LivePoints(gctx, gw.Event)
			if err != nil {
				return nil
			}
			stats := make(map[int]fpl.LiveStats, len(lv.Elements))
			for _, el := range lv.Elements {
				stats[el.ID] = el.Stats
			}
			mu.Lock()
			picks[gw.Event], live[gw.Event] = pk, stats
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait() // every goroutine returns nil
	return picks, live
}

// captainResult is one gameweek's captaincy outcome.
type captainResult struct {
	effective HistoryCaptain // who the multiplier applied to
	best      HistoryCaptain // best scorer in the counting XI
	forgone   int
	viceID    int
	note      string
}

// captainReturn works out a gameweek's captaincy from its picks and live
// stats. See the package comment above for the rules.
func captainReturn(p *fpl.TeamPicks, live map[int]fpl.LiveStats, names map[int]string) captainResult {
	var captain, vice *fpl.Pick
	for i := range p.Picks {
		switch {
		case p.Picks[i].IsCaptain:
			captain = &p.Picks[i]
		case p.Picks[i].IsViceCaptain:
			vice = &p.Picks[i]
		}
	}
	var r captainResult
	if captain == nil {
		return r
	}
	if vice != nil {
		r.viceID = vice.Element
	}

	// The multiplier is 2, or 3 under a Triple Captain.
	multiplier := 2
	if p.ActiveChip != nil && *p.ActiveChip == "3xc" {
		multiplier = 3
	}

	effective := captain.Element
	if live[captain.Element].Minutes == 0 && vice != nil && live[vice.Element].Minutes > 0 {
		effective = vice.Element
		r.note = fmt.Sprintf("%s didn't play, so the armband passed to %s.", names[captain.Element], names[vice.Element])
	}
	r.effective = HistoryCaptain{Name: names[effective], Points: live[effective].TotalPoints}

	// The XI that counted: starters, with automatic substitutions applied,
	// or the whole squad under a Bench Boost.
	counted := make([]int, 0, 15)
	benchBoost := p.ActiveChip != nil && *p.ActiveChip == "bboost"
	for _, pk := range p.Picks {
		if pk.Position <= 11 || benchBoost {
			counted = append(counted, pk.Element)
		}
	}
	for _, s := range p.AutomaticSubs {
		if i := slices.Index(counted, s.ElementOut); i >= 0 {
			counted[i] = s.ElementIn
		}
	}

	best := effective
	for _, id := range counted {
		if live[id].TotalPoints > live[best].TotalPoints {
			best = id
		}
	}
	r.best = HistoryCaptain{Name: names[best], Points: live[best].TotalPoints}
	r.forgone = (r.best.Points - r.effective.Points) * (multiplier - 1)
	return r
}
