// Package store reads and writes the on-disk artifacts that make the
// captaincy weights adaptive rather than fixed forever: per-gameweek player
// snapshots, cached live results, and the optimized weight set derived from
// them.
//
// These schemas are a compatibility boundary, not an implementation detail.
// A GW snapshot captured before a deadline records exactly what a manager saw
// at decision time — form, price, fixture — and that moment cannot be
// recreated once the gameweek has been played. Whatever writes these files
// must keep writing the same shape indefinitely, Go or Python.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

// Layout mirrors the Python project's data/ directory so the two
// implementations can share a single set of files on disk.
type Layout struct {
	Root string // project root; snapshots live under Root/data/...
}

func (l Layout) snapshotDir() string      { return filepath.Join(l.Root, "data", "snapshots") }
func (l Layout) backtestCacheDir() string { return filepath.Join(l.Root, "data", "backtest_cache") }
func (l Layout) SnapshotPath(gw int) string {
	return filepath.Join(l.snapshotDir(), fmt.Sprintf("gw%d.json", gw))
}
func (l Layout) LiveDataPath(gw int) string {
	return filepath.Join(l.backtestCacheDir(), fmt.Sprintf("live_gw%d.json", gw))
}
func (l Layout) FixturesCachePath() string {
	return filepath.Join(l.backtestCacheDir(), "fixtures.json")
}
func (l Layout) OptimizedWeightsPath() string {
	return filepath.Join(l.Root, "data", "optimized_weights.json")
}

// Snapshot is one gameweek's captured player and team state, as written to
// data/snapshots/gw{N}.json before that gameweek's deadline.
//
// It deliberately holds a narrower field set than the full FPL bootstrap —
// only what a backtest needs to reconstruct "what did we know at the time" —
// so a season's worth of snapshots stays well under the size of one full
// bootstrap fetch.
type Snapshot struct {
	Gameweek     int              `json:"gameweek"`
	CapturedAt   string           `json:"captured_at"`
	IsBackfill   bool             `json:"is_backfill"`
	Event        SnapshotEvent    `json:"event"`
	Players      []SnapshotPlayer `json:"players"`
	Teams        []SnapshotTeam   `json:"teams"`
	FixtureCount int              `json:"fixture_count"`
}

type SnapshotEvent struct {
	ID           *int   `json:"id"`
	DeadlineTime string `json:"deadline_time"`
	Finished     bool   `json:"finished"`
	DataChecked  bool   `json:"data_checked"`
}

// SnapshotPlayer is the subset of bootstrap.elements fields a backtest needs.
// Numeric fields use fpl.Num for the same reason as the live client: FPL (and
// therefore every snapshot captured from it) writes them as strings.
type SnapshotPlayer struct {
	ID          int    `json:"id"`
	WebName     string `json:"web_name"`
	FirstName   string `json:"first_name"`
	SecondName  string `json:"second_name"`
	Team        int    `json:"team"`
	ElementType int    `json:"element_type"`

	Form          fpl.Num `json:"form"`
	PointsPerGame fpl.Num `json:"points_per_game"`
	EPNext        fpl.Num `json:"ep_next"`
	EPThis        fpl.Num `json:"ep_this"`
	TotalPoints   int     `json:"total_points"`
	Minutes       int     `json:"minutes"`
	Starts        int     `json:"starts"`

	ExpectedGoals              fpl.Num `json:"expected_goals"`
	ExpectedAssists            fpl.Num `json:"expected_assists"`
	ExpectedGoalInvolvements   fpl.Num `json:"expected_goal_involvements"`
	ExpectedGoalsConceded      fpl.Num `json:"expected_goals_conceded"`
	ExpectedGoalsConcededPer90 fpl.Num `json:"expected_goals_conceded_per_90"`

	ICTIndex   fpl.Num `json:"ict_index"`
	Influence  fpl.Num `json:"influence"`
	Creativity fpl.Num `json:"creativity"`
	Threat     fpl.Num `json:"threat"`

	Bonus       int `json:"bonus"`
	BPS         int `json:"bps"`
	GoalsScored int `json:"goals_scored"`
	Assists     int `json:"assists"`
	CleanSheets int `json:"clean_sheets"`

	NowCost           int     `json:"now_cost"`
	SelectedByPercent fpl.Num `json:"selected_by_percent"`

	Status                   string `json:"status"`
	ChanceOfPlayingNextRound *int   `json:"chance_of_playing_next_round"`

	PenaltiesOrder                   *int `json:"penalties_order"`
	CornersAndIndirectFreekicksOrder *int `json:"corners_and_indirect_freekicks_order"`
	DirectFreekicksOrder             *int `json:"direct_freekicks_order"`

	TransfersInEvent  int `json:"transfers_in_event"`
	TransfersOutEvent int `json:"transfers_out_event"`
}

// SnapshotTeam is the subset of bootstrap.teams fields captured per gameweek.
type SnapshotTeam struct {
	ID                  int    `json:"id"`
	Name                string `json:"name"`
	ShortName           string `json:"short_name"`
	Strength            *int   `json:"strength"`
	StrengthOverallHome int    `json:"strength_overall_home"`
	StrengthOverallAway int    `json:"strength_overall_away"`
	StrengthAttackHome  int    `json:"strength_attack_home"`
	StrengthAttackAway  int    `json:"strength_attack_away"`
	StrengthDefenceHome int    `json:"strength_defence_home"`
	StrengthDefenceAway int    `json:"strength_defence_away"`
}

// LiveData is the raw GET /event/{gw}/live/ response, cached verbatim to
// data/backtest_cache/live_gw{N}.json so a backtest run doesn't refetch it.
//
// This is the same wire shape the live client and the live-scoring algorithm
// use, so it is an alias onto fpl.LiveResponse rather than a second
// definition — ActualPoints and every other method come along for free.
type LiveData = fpl.LiveResponse
type LiveElement = fpl.LiveElement

// readJSON loads path into T, returning (zero, false, nil) rather than an
// error when the file is simply absent — every caller in this package treats
// a missing snapshot as "not available yet," not a failure.
func readJSON[T any](path string) (T, bool, error) {
	var out T
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, false, nil
		}
		return out, false, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, false, fmt.Errorf("decode %s: %w", path, err)
	}
	return out, true, nil
}

// LoadSnapshot reads data/snapshots/gw{N}.json if it exists.
func (l Layout) LoadSnapshot(gw int) (*Snapshot, bool, error) {
	s, ok, err := readJSON[Snapshot](l.SnapshotPath(gw))
	if !ok || err != nil {
		return nil, ok, err
	}
	return &s, true, nil
}

// LoadLiveData reads data/backtest_cache/live_gw{N}.json if it exists.
func (l Layout) LoadLiveData(gw int) (*LiveData, bool, error) {
	d, ok, err := readJSON[LiveData](l.LiveDataPath(gw))
	if !ok || err != nil {
		return nil, ok, err
	}
	return &d, true, nil
}

// LoadFixturesCache reads data/backtest_cache/fixtures.json if it exists —
// the same wire shape as the live /fixtures/ endpoint, just persisted so
// repeated backtest runs don't refetch it.
func (l Layout) LoadFixturesCache() ([]fpl.Fixture, bool, error) {
	return readJSON[[]fpl.Fixture](l.FixturesCachePath())
}

// SnapshotFromBootstrap ports snapshot_gw.py's compact field selection: only
// what a backtest needs, not the full ~4MB bootstrap.
func SnapshotFromBootstrap(b *fpl.Bootstrap, fixtures []fpl.Fixture, gw int, backfill bool, capturedAt time.Time) Snapshot {
	players := make([]SnapshotPlayer, 0, len(b.Elements))
	for _, p := range b.Elements {
		players = append(players, SnapshotPlayer{
			ID:                               p.ID,
			WebName:                          p.WebName,
			FirstName:                        p.FirstName,
			SecondName:                       p.SecondName,
			Team:                             p.Team,
			ElementType:                      p.ElementType,
			Form:                             p.Form,
			PointsPerGame:                    p.PointsPerGame,
			EPNext:                           p.EPNext,
			EPThis:                           p.EPThis,
			TotalPoints:                      p.TotalPoints,
			Minutes:                          p.Minutes,
			Starts:                           p.Starts,
			ExpectedGoals:                    p.ExpectedGoals,
			ExpectedAssists:                  p.ExpectedAssists,
			ExpectedGoalInvolvements:         p.ExpectedGoalInvolvements,
			ExpectedGoalsConceded:            p.ExpectedGoalsConceded,
			ExpectedGoalsConcededPer90:       p.ExpectedGoalsConcededPer90,
			ICTIndex:                         p.ICTIndex,
			Influence:                        p.Influence,
			Creativity:                       p.Creativity,
			Threat:                           p.Threat,
			Bonus:                            p.Bonus,
			BPS:                              p.BPS,
			GoalsScored:                      p.GoalsScored,
			Assists:                          p.Assists,
			CleanSheets:                      p.CleanSheets,
			NowCost:                          p.NowCost,
			SelectedByPercent:                p.SelectedByPercent,
			Status:                           p.Status,
			ChanceOfPlayingNextRound:         p.ChanceOfPlayingNextRound,
			PenaltiesOrder:                   p.PenaltiesOrder,
			CornersAndIndirectFreekicksOrder: p.CornersAndIndirectFreekicksOrder,
			DirectFreekicksOrder:             p.DirectFreekicksOrder,
			TransfersInEvent:                 p.TransfersInEvent,
			TransfersOutEvent:                p.TransfersOutEvent,
		})
	}

	teams := make([]SnapshotTeam, 0, len(b.Teams))
	for _, t := range b.Teams {
		teams = append(teams, SnapshotTeam{
			ID:                  t.ID,
			Name:                t.Name,
			ShortName:           t.ShortName,
			Strength:            t.Strength,
			StrengthOverallHome: t.StrengthOverallHome,
			StrengthOverallAway: t.StrengthOverallAway,
			StrengthAttackHome:  t.StrengthAttackHome,
			StrengthAttackAway:  t.StrengthAttackAway,
			StrengthDefenceHome: t.StrengthDefenceHome,
			StrengthDefenceAway: t.StrengthDefenceAway,
		})
	}

	// event defaults to the zero value (nil ID, false Finished/DataChecked) if
	// gw isn't in the bootstrap's event list — mirrors Python's
	// next((e for e in events if e["id"] == target_gw), {}).get(...).
	var event SnapshotEvent
	for _, e := range b.Events {
		if e.ID == gw {
			id := e.ID
			event = SnapshotEvent{ID: &id, DeadlineTime: e.DeadlineTime, Finished: e.Finished, DataChecked: e.DataChecked}
			break
		}
	}

	fixtureCount := 0
	for _, f := range fixtures {
		if f.InGameweek(gw) {
			fixtureCount++
		}
	}

	return Snapshot{
		Gameweek:     gw,
		CapturedAt:   capturedAt.UTC().Format(time.RFC3339),
		IsBackfill:   backfill,
		Event:        event,
		Players:      players,
		Teams:        teams,
		FixtureCount: fixtureCount,
	}
}

// SaveSnapshot writes s to data/snapshots/gw{N}.json, creating the directory
// if needed. Unlike SaveOptimizedWeightsCache this is compact (no indent),
// matching snapshot_gw.py's json.dump(snapshot, f) with no indent argument —
// a season's worth of these adds up, and nothing reads the file by eye.
func (l Layout) SaveSnapshot(s Snapshot) error {
	path := l.SnapshotPath(s.Gameweek)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
