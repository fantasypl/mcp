package algo

import (
	"context"
	"fmt"
	"slices"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

// Live scoring reports a manager's squad during an active gameweek: points so
// far, bonus points confirmed or projected from live BPS standings, automatic
// substitutions the system would trigger for any starter stuck on 0 minutes,
// and a rough sense of where that score sits against the gameweek average.
//
// Bonus points are the one piece of this that changes meaning mid-match: FPL
// awards them from BPS rankings only after a fixture finishes, so live_points
// under-counts until then. This mirrors that by projecting bonus from BPS
// while a match is in progress and switching to the confirmed figure once
// FPL's own stats.bonus is populated.

const (
	bonusFirst  = 3
	bonusSecond = 2
	bonusThird  = 1
)

var bonusPool = []int{bonusFirst, bonusSecond, bonusThird}

// bpsEntry is one player's BPS standing within a single fixture.
type bpsEntry struct {
	ElementID      int    `json:"element_id"`
	Name           string `json:"name"`
	Team           string `json:"team"`
	BPS            int    `json:"bps"`
	ProjectedBonus int    `json:"projected_bonus"`
	BPSRank        int    `json:"bps_rank"`
}

type bonusInfo struct {
	BPS            int
	ProjectedBonus int
	BPSRank        int
	BPSBehindBonus int
	Match          string
}

// calculateFixtureBPS ranks every player in one fixture by BPS and assigns
// projected bonus under FPL's tie rule: 3/2/1 to the top three BPS values,
// with ties sharing the higher bonus and consuming that many slots — two
// players tied for first both get 3, and the next player gets 1, not 2.
//
// Tie order beyond BPS itself is not something the reference implementation
// actually controls — CPython set iteration order for player IDs is
// implementation-defined, so there was never a single "correct" order to
// match byte-for-byte here. Every player in a tie group receives the same
// bonus and rank regardless of order, which is the only thing that's
// observable; this breaks ties by element ID so the output is at least
// deterministic across runs, which the Python's own version is not.
func calculateFixtureBPS(playerIDs []int, live fpl.LiveResponse, playersByID map[int]*fpl.Player, teams map[int]*fpl.Team) []bpsEntry {
	liveByID := live.ByID()

	entries := make([]bpsEntry, 0, len(playerIDs))
	for _, pid := range playerIDs {
		el, ok := liveByID[pid]
		if !ok || el.Stats.Minutes == 0 {
			continue
		}
		p := playersByID[pid]
		name, team := "Unknown", "?"
		if p != nil {
			name = p.WebName
			team = shortName(teams[p.Team])
		}
		entries = append(entries, bpsEntry{ElementID: pid, Name: name, Team: team, BPS: el.Stats.BPS})
	}

	slices.SortStableFunc(entries, func(a, b bpsEntry) int {
		switch {
		case a.BPS != b.BPS:
			return b.BPS - a.BPS
		default:
			return a.ElementID - b.ElementID
		}
	})

	poolIdx := 0
	rank := 1
	i := 0
	for i < len(entries) && poolIdx < len(bonusPool) {
		currentBPS := entries[i].BPS
		j := i
		for j < len(entries) && entries[j].BPS == currentBPS {
			j++
		}
		bonusValue := bonusPool[poolIdx]
		for k := i; k < j; k++ {
			entries[k].ProjectedBonus = bonusValue
			entries[k].BPSRank = rank
		}
		poolIdx += j - i
		rank += j - i
		i = j
	}
	for i < len(entries) {
		currentBPS := entries[i].BPS
		j := i
		for j < len(entries) && entries[j].BPS == currentBPS {
			j++
		}
		for k := i; k < j; k++ {
			entries[k].ProjectedBonus = 0
			entries[k].BPSRank = rank
		}
		rank += j - i
		i = j
	}

	return entries
}

type MatchBPS struct {
	FixtureID int        `json:"fixture_id"`
	Match     string     `json:"match"`
	Status    string     `json:"status"` // not_started | live | finished
	TopBPS    []bpsEntry `json:"top_bps"`
}

// buildBPSData computes BPS-derived bonus projections for every fixture in
// the gameweek, returning both the per-match summaries and a flat lookup of
// every player's bonus standing.
func buildBPSData(fixtures []fpl.Fixture, gw int, live fpl.LiveResponse, playersByID map[int]*fpl.Player, teams map[int]*fpl.Team) ([]MatchBPS, map[int]*bonusInfo) {
	gwFixtures := make([]*fpl.Fixture, 0, 10)
	for i := range fixtures {
		if fixtures[i].InGameweek(gw) {
			gwFixtures = append(gwFixtures, &fixtures[i])
		}
	}

	teamToFixture := make(map[int]int, len(gwFixtures)*2)
	fixturePlayers := make(map[int][]int)
	for _, f := range gwFixtures {
		teamToFixture[f.TeamH] = f.ID
		teamToFixture[f.TeamA] = f.ID
	}
	// Deterministic order: iterate players by ID rather than a live-stats map.
	ids := make([]int, 0, len(live.Elements))
	for _, e := range live.Elements {
		ids = append(ids, e.ID)
	}
	slices.Sort(ids)
	for _, pid := range ids {
		p := playersByID[pid]
		if p == nil {
			continue
		}
		if fid, ok := teamToFixture[p.Team]; ok {
			fixturePlayers[fid] = append(fixturePlayers[fid], pid)
		}
	}

	matchBPS := make([]MatchBPS, 0, len(gwFixtures))
	allBonus := make(map[int]*bonusInfo)

	for _, f := range gwFixtures {
		homeName := shortName(teams[f.TeamH])
		awayName := shortName(teams[f.TeamA])
		match := fmt.Sprintf("%s vs %s", homeName, awayName)

		if !f.Started {
			matchBPS = append(matchBPS, MatchBPS{FixtureID: f.ID, Match: match, Status: "not_started", TopBPS: []bpsEntry{}})
			continue
		}

		rankings := calculateFixtureBPS(fixturePlayers[f.ID], live, playersByID, teams)

		// The bonus cutoff is the lowest BPS that still earned bonus — the
		// last entry in ranking order with ProjectedBonus >= 1, matching the
		// Python's "keep overwriting while scanning" loop exactly.
		bonusCutoff := -1
		hasCutoff := false
		for _, r := range rankings {
			if r.ProjectedBonus >= 1 {
				bonusCutoff = r.BPS
				hasCutoff = true
			}
		}

		for _, r := range rankings {
			info := &bonusInfo{BPS: r.BPS, ProjectedBonus: r.ProjectedBonus, BPSRank: r.BPSRank, Match: match}
			if r.ProjectedBonus == 0 && len(rankings) > 0 && hasCutoff {
				info.BPSBehindBonus = bonusCutoff - r.BPS
			}
			allBonus[r.ElementID] = info
		}

		status := "live"
		if f.Finished || f.FinishedProvisional {
			status = "finished"
		}
		top := rankings
		if len(top) > 5 {
			top = top[:5]
		}
		matchBPS = append(matchBPS, MatchBPS{FixtureID: f.ID, Match: match, Status: status, TopBPS: top})
	}

	return matchBPS, allBonus
}

func bonusNarrative(info *bonusInfo) string {
	if info == nil {
		return "No BPS data available"
	}
	switch {
	case info.ProjectedBonus > 0:
		return fmt.Sprintf("On track for %d bonus (%d BPS, rank %d)", info.ProjectedBonus, info.BPS, info.BPSRank)
	case info.BPSBehindBonus > 0:
		return fmt.Sprintf("%d BPS behind bonus (%d BPS, rank %d)", info.BPSBehindBonus, info.BPS, info.BPSRank)
	default:
		return fmt.Sprintf("Not in bonus contention (%d BPS)", info.BPS)
	}
}

type LivePointsResult struct {
	TeamID           int            `json:"team_id"`
	Gameweek         int            `json:"gameweek"`
	ActiveChip       *string        `json:"active_chip"`
	LiveTotal        int            `json:"live_total"`
	GameweekAverage  int            `json:"gameweek_average"`
	HighestScore     *int           `json:"highest_score"`
	PointsVsAverage  float64        `json:"points_vs_average"`
	RankEstimate     string         `json:"rank_estimate"`
	TopScorer        *LiveTopScorer `json:"top_scorer"`
	SquadValid       bool           `json:"squad_valid"`
	NumStarters      int            `json:"num_starters"`
	Starters         []LivePlayer   `json:"starters"`
	NumBench         int            `json:"num_bench"`
	Bench            []LivePlayer   `json:"bench"`
	AutoSubScenarios []AutoSub      `json:"auto_sub_scenarios"`
	MatchBPS         []MatchBPS     `json:"match_bps"`
	BonusStatus      string         `json:"bonus_status"`
	Note             string         `json:"note"`
}

type LiveTopScorer struct {
	Name   string `json:"name"`
	Team   string `json:"team"`
	Points int    `json:"points"`
}

type LivePlayer struct {
	ElementID         int             `json:"element_id"`
	Name              string          `json:"name"`
	Team              string          `json:"team"`
	Position          string          `json:"position"`
	IsCaptain         bool            `json:"is_captain"`
	IsViceCaptain     bool            `json:"is_vice_captain"`
	Multiplier        int             `json:"multiplier"`
	LivePoints        int             `json:"live_points"`
	ContributedPoints int             `json:"contributed_points"`
	ProjectedBonus    int             `json:"projected_bonus"`
	ConfirmedBonus    int             `json:"confirmed_bonus"`
	BonusStatus       string          `json:"bonus_status"`
	BonusProjection   BonusProjection `json:"bonus_projection"`
	MinutesPlayed     int             `json:"minutes_played"`
	Played            bool            `json:"played"`
	ChanceOfPlaying   *int            `json:"chance_of_playing"`
}

type BonusProjection struct {
	BPS            int    `json:"bps"`
	ProjectedBonus int    `json:"projected_bonus"`
	BPSRank        *int   `json:"bps_rank"`
	BPSBehindBonus int    `json:"bps_behind_bonus"`
	Match          string `json:"match"`
	Narrative      string `json:"narrative"`
}

type AutoSub struct {
	Out          string `json:"out"`
	In           string `json:"in"`
	PointsGained int    `json:"points_gained"`
	Note         string `json:"note"`
}

// LivePoints ports live.get_live_points.
//
// Note: the Python fetches the manager's team history alongside everything
// else but never reads it — dead code preserved nowhere here, since skipping
// an unused fetch changes no observable output and saves a network round
// trip.
func (e *Engine) LivePoints(ctx context.Context, teamID int) (*LivePointsResult, error) {
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	currentGW := bootstrap.CurrentGameweek()

	picks, err := e.client.TeamPicks(ctx, teamID, currentGW)
	if err != nil {
		return nil, err
	}
	live, err := e.client.LivePoints(ctx, currentGW)
	if err != nil {
		return nil, err
	}
	eventStatus, err := e.client.EventStatus(ctx)
	if err != nil {
		return nil, err
	}
	fixtures, err := e.client.Fixtures(ctx)
	if err != nil {
		return nil, err
	}

	playersByID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		playersByID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}
	teams := teamsByID(bootstrap)
	liveByID := live.ByID()

	matchBPS, allBonus := buildBPSData(fixtures, currentGW, *live, playersByID, teams)

	enrich := func(pick fpl.Pick) LivePlayer {
		p := playersByID[pick.Element]
		el := liveByID[pick.Element]
		pts := el.Stats.TotalPoints
		confirmedBonus := el.Stats.Bonus

		var projectedBonus int
		bonusStatus := "projected"
		info := allBonus[pick.Element]
		if confirmedBonus > 0 {
			projectedBonus = 0
			bonusStatus = "confirmed"
		} else if info != nil {
			projectedBonus = info.ProjectedBonus
		}

		name, team, position := "Unknown", "?", "?"
		var chance *int
		if p != nil {
			name = p.WebName
			team = shortName(teams[p.Team])
			position = Position(p.ElementType)
			chance = p.ChanceOfPlayingThisRound
		}

		narrative := bonusNarrative(info)
		if bonusStatus == "confirmed" {
			narrative = fmt.Sprintf("Bonus confirmed: %d pts", confirmedBonus)
		}

		var bpsRank *int
		bps, behind, match := 0, 0, ""
		if info != nil {
			bps, behind, match = info.BPS, info.BPSBehindBonus, info.Match
			if info.BPSRank != 0 {
				r := info.BPSRank
				bpsRank = &r
			}
		}

		return LivePlayer{
			ElementID: pick.Element, Name: name, Team: team, Position: position,
			IsCaptain: pick.IsCaptain, IsViceCaptain: pick.IsViceCaptain, Multiplier: pick.Multiplier,
			LivePoints: pts, ContributedPoints: pts * pick.Multiplier,
			ProjectedBonus: projectedBonus, ConfirmedBonus: confirmedBonus, BonusStatus: bonusStatus,
			BonusProjection: BonusProjection{
				BPS: bps, ProjectedBonus: projectedBonus, BPSRank: bpsRank,
				BPSBehindBonus: behind, Match: match, Narrative: narrative,
			},
			MinutesPlayed: el.Stats.Minutes, Played: el.Stats.Minutes > 0,
			ChanceOfPlaying: chance,
		}
	}

	starterPicks := picks.Starters()
	benchPicks := picks.Bench()
	starters := make([]LivePlayer, 0, len(starterPicks))
	for _, p := range starterPicks {
		starters = append(starters, enrich(p))
	}
	bench := make([]LivePlayer, 0, len(benchPicks))
	for _, p := range benchPicks {
		bench = append(bench, enrich(p))
	}

	totalLive := 0
	for _, s := range starters {
		totalLive += s.ContributedPoints
	}

	// Auto-sub detection, in bench order: a GKP starter with 0 minutes can
	// only be replaced by the bench GKP; an outfield starter by the first
	// bench player (excluding the GKP) who actually played.
	var autoSubs []AutoSub
	usedBench := make(map[int]bool)
	for _, starter := range starters {
		if starter.MinutesPlayed != 0 {
			continue
		}
		if starter.Position == "GKP" {
			for _, b := range bench {
				if b.Position == "GKP" && b.Played && !usedBench[b.ElementID] {
					autoSubs = append(autoSubs, AutoSub{Out: starter.Name, In: b.Name, PointsGained: b.LivePoints, Note: "GKP auto-sub"})
					usedBench[b.ElementID] = true
					break
				}
			}
			continue
		}
		for _, b := range bench {
			if b.Position == "GKP" {
				continue
			}
			if b.Played && !usedBench[b.ElementID] {
				autoSubs = append(autoSubs, AutoSub{Out: starter.Name, In: b.Name, PointsGained: b.LivePoints, Note: "Auto-sub (bench order)"})
				usedBench[b.ElementID] = true
				break
			}
		}
	}
	if autoSubs == nil {
		autoSubs = []AutoSub{}
	}

	avgPoints := 50
	var highestScore *int
	var topScorer *LiveTopScorer
	for i := range bootstrap.Events {
		ev := &bootstrap.Events[i]
		if ev.ID != currentGW {
			continue
		}
		avgPoints = ev.AverageEntryScore
		highestScore = ev.HighestScore
		if ev.TopElement != nil {
			if p := playersByID[*ev.TopElement]; p != nil {
				topScorer = &LiveTopScorer{
					Name: p.WebName, Team: shortName(teams[p.Team]),
					Points: liveByID[*ev.TopElement].Stats.TotalPoints,
				}
			}
		}
		break
	}
	pointsVsAvg := Round(float64(totalLive-avgPoints), 1)

	rankEstimate := "Average"
	switch {
	case pointsVsAvg > 5:
		rankEstimate = "Above average"
	case pointsVsAvg < -5:
		rankEstimate = "Below average"
	}

	bonusConfirmed := eventStatus.BonusConfirmed()
	note := "Live points update during matches. Bonus points are projected and may change."
	if bonusConfirmed {
		note = "Live points update during matches. Bonus points are confirmed."
	}

	return &LivePointsResult{
		TeamID: teamID, Gameweek: currentGW, ActiveChip: picks.ActiveChip,
		LiveTotal: totalLive, GameweekAverage: avgPoints, HighestScore: highestScore,
		PointsVsAverage: pointsVsAvg, RankEstimate: rankEstimate, TopScorer: topScorer,
		SquadValid:  len(starters) == 11 && len(bench) == 4,
		NumStarters: len(starters), Starters: starters,
		NumBench: len(bench), Bench: bench,
		AutoSubScenarios: autoSubs, MatchBPS: matchBPS,
		BonusStatus: ifStr(bonusConfirmed, "confirmed", "provisional"),
		Note:        note,
	}, nil
}

func ifStr(cond bool, t, f string) string {
	if cond {
		return t
	}
	return f
}
