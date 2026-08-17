// Package vaastav reconstructs point-in-time FPL state from
// vaastav/Fantasy-Premier-League's per-gameweek CSVs, for backtesting the
// algorithms against real historical outcomes across many seasons.
//
// Why reconstruction rather than replaying today's bootstrap against past
// gameweeks: the latter is look-ahead biased — "predicting" GW10 using
// season totals that already include GW11-38. vaastav's per-gameweek CSVs
// (data/{season}/gws/gw{N}.csv) are match-level rows, not running totals, so
// summing gw1..gw{N-1} recovers exactly what a manager would have seen going
// into gameweek N.
//
// Known gaps versus a live run, all on small-weight scoring terms:
//   - ep_next (FPL's own point prediction) isn't published outside the live
//     API and can't be reconstructed; left at 0.
//   - Set-piece and penalty duty (penalties_order etc.) are roster metadata,
//     not match output; left nil, so nobody gets the set-piece bonus.
//   - status / chance_of_playing can't be reconstructed either; everyone is
//     treated as available. This is largely self-correcting — a player who
//     wasn't actually playing already has near-zero recent form.
//   - Team strength ratings come from a single current-season teams.csv
//     rather than a point-in-time one; strength moves slowly, so this is a
//     minor approximation.
package vaastav

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
)

// DefaultBaseURL is vaastav's raw-content root, overridable on Corpus for
// tests.
const DefaultBaseURL = "https://raw.githubusercontent.com/vaastav/Fantasy-Premier-League/HEAD/data"

// Seasons this package can reconstruct, oldest first.
//
// vaastav's corpus goes back to 2016-17, but the per-gameweek CSV schema
// changed underneath it: verified directly against the raw files, gws/gw*.csv
// only gained "position" and "team" columns in 2020-21 (earlier seasons carry
// "opponent_team" and a match "fixture" id instead, requiring a different
// join strategy), and 2016-17/2017-18/2018-19 don't have a teams.csv at all
// (404). BuildCase fails loudly rather than silently defaulting a missing
// position/team to zero, so seasons before 2020-21 are excluded here rather
// than left in for callers to discover the failure themselves.
var Seasons = []string{
	"2020-21", "2021-22", "2022-23", "2023-24", "2024-25", "2025-26",
}

var positionToElementType = map[string]int{"GKP": 1, "GK": 1, "DEF": 2, "MID": 3, "FWD": 4}

// sumFields are match-level stats summed into season-to-date totals.
var sumFields = []string{
	"total_points", "minutes", "starts", "goals_scored", "assists", "bonus",
	"bps", "clean_sheets", "yellow_cards", "red_cards", "expected_goals",
	"expected_assists", "expected_goal_involvements", "ict_index",
	"influence", "creativity", "threat",
}

// ActualEntry is a player's real result in a single gameweek.
type ActualEntry struct {
	WebName  string
	Team     string
	Position string
	Points   int
	Minutes  int
}

// Case is a reconstructed point-in-time backtest input: state through
// predictGW-1, the full fixture list, and predictGW's actual results.
type Case struct {
	Season    string
	PredictGW int
	Bootstrap *fpl.Bootstrap
	Fixtures  []fpl.Fixture
	Actual    map[int]ActualEntry
}

// Corpus fetches and disk-caches vaastav's per-season CSVs, and reconstructs
// backtest Cases from them.
type Corpus struct {
	CacheDir string
	BaseURL  string
	HTTP     *http.Client
}

// NewCorpus returns a Corpus caching fetched CSVs under cacheDir.
func NewCorpus(cacheDir string) *Corpus {
	return &Corpus{CacheDir: cacheDir, BaseURL: DefaultBaseURL, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// fetch returns relPath's bytes, from the on-disk cache if present, else
// from GitHub, caching the result. vaastav's historical files never change
// once a season is over, so there is no TTL — only the current season's
// tail files would ever legitimately go stale, and rerunning the corpus
// build with a fresh cache dir handles that case.
func (c *Corpus) fetch(ctx context.Context, relPath string) ([]byte, error) {
	cachePath := filepath.Join(c.CacheDir, relPath)
	if b, err := os.ReadFile(cachePath); err == nil {
		return b, nil
	}

	url := c.BaseURL + "/" + relPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return nil, fmt.Errorf("cache %s: %w", relPath, err)
	}
	if err := os.WriteFile(cachePath, b, 0o644); err != nil {
		return nil, fmt.Errorf("cache %s: %w", relPath, err)
	}
	return b, nil
}

func (c *Corpus) csvRows(ctx context.Context, relPath string) ([]map[string]string, error) {
	b, err := c.fetch(ctx, relPath)
	if err != nil {
		return nil, err
	}
	r := csv.NewReader(strings.NewReader(string(b)))
	r.FieldsPerRecord = -1 // vaastav rows are occasionally ragged across seasons/columns
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", relPath, err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	header := records[0]
	rows := make([]map[string]string, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := make(map[string]string, len(header))
		for i, h := range header {
			if i < len(rec) {
				row[h] = rec[i]
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// num parses a vaastav CSV cell as a float, treating "", "None", and
// unparseable values as 0 — vaastav writes missing values both ways
// depending on column and season.
func num(s string) float64 {
	if s == "" || s == "None" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func numInt(s string) int { return int(num(s)) }

func numIntDefault(s string, def int) int {
	if s == "" || s == "None" {
		return def
	}
	return numInt(s)
}

// requireColumns reports an error naming the first missing column, so a
// season with an incompatible CSV schema fails immediately rather than
// silently producing players with a zero-value team/position.
func requireColumns(row map[string]string, cols ...string) error {
	for _, c := range cols {
		if _, ok := row[c]; !ok {
			return fmt.Errorf("missing column %q", c)
		}
	}
	return nil
}

type teamRow struct {
	id, code                                 int
	name, shortName                          string
	strength, position, played, points       int
	strengthOverallHome, strengthOverallAway int
	strengthAttackHome, strengthAttackAway   int
	strengthDefenceHome, strengthDefenceAway int
}

func (c *Corpus) loadTeams(ctx context.Context, season string) (map[int]teamRow, map[string]int, error) {
	rows, err := c.csvRows(ctx, season+"/teams.csv")
	if err != nil {
		return nil, nil, fmt.Errorf("load teams: %w", err)
	}
	byID := make(map[int]teamRow, len(rows))
	nameToID := make(map[string]int, len(rows))
	for _, r := range rows {
		id := numInt(r["id"])
		tr := teamRow{
			id: id, code: numInt(r["code"]), name: r["name"], shortName: r["short_name"],
			strength: numInt(r["strength"]), position: numInt(r["position"]),
			played: numInt(r["played"]), points: numInt(r["points"]),
			strengthOverallHome: numIntDefault(r["strength_overall_home"], 1200),
			strengthOverallAway: numIntDefault(r["strength_overall_away"], 1200),
			strengthAttackHome:  numIntDefault(r["strength_attack_home"], 1200),
			strengthAttackAway:  numIntDefault(r["strength_attack_away"], 1200),
			strengthDefenceHome: numIntDefault(r["strength_defence_home"], 1200),
			strengthDefenceAway: numIntDefault(r["strength_defence_away"], 1200),
		}
		byID[id] = tr
		nameToID[r["name"]] = id
	}
	return byID, nameToID, nil
}

func (c *Corpus) loadFixtures(ctx context.Context, season string) ([]fpl.Fixture, error) {
	rows, err := c.csvRows(ctx, season+"/fixtures.csv")
	if err != nil {
		return nil, fmt.Errorf("load fixtures: %w", err)
	}
	var out []fpl.Fixture
	for _, r := range rows {
		if r["event"] == "" || r["event"] == "None" {
			continue
		}
		event := numInt(r["event"])
		out = append(out, fpl.Fixture{
			ID:              numInt(r["id"]),
			Event:           &event,
			TeamH:           numInt(r["team_h"]),
			TeamA:           numInt(r["team_a"]),
			TeamHDifficulty: numIntDefault(r["team_h_difficulty"], 3),
			TeamADifficulty: numIntDefault(r["team_a_difficulty"], 3),
			Finished:        r["finished"] == "True",
			Started:         r["started"] == "True",
			KickoffTime:     r["kickoff_time"],
			Minutes:         numInt(r["minutes"]),
		})
	}
	return out, nil
}

// buildState sums gws/gw1.csv..gw{uptoGW}.csv into season-to-date player
// state, the reconstructed equivalent of a live bootstrap fetched just
// before gameweek uptoGW+1's deadline.
func (c *Corpus) buildState(ctx context.Context, season string, uptoGW int, nameToID map[string]int) ([]fpl.Player, error) {
	type totals struct {
		sum     map[string]float64
		name    string
		pos     string
		team    string
		nowCost int
		played  int // gameweeks with minutes > 0
	}
	byElement := make(map[int]*totals)

	windowStart := uptoGW - 4
	if windowStart < 1 {
		windowStart = 1
	}
	recentPoints := make(map[int][]float64)

	for gw := 1; gw <= uptoGW; gw++ {
		rows, err := c.csvRows(ctx, fmt.Sprintf("%s/gws/gw%d.csv", season, gw))
		if err != nil {
			return nil, fmt.Errorf("load gw%d: %w", gw, err)
		}
		if gw == 1 && len(rows) > 0 {
			if err := requireColumns(rows[0], "position", "team"); err != nil {
				return nil, fmt.Errorf("%s: %w (season predates vaastav's position/team columns, introduced 2020-21 — see Seasons doc)", season, err)
			}
		}
		for _, row := range rows {
			eid := numInt(row["element"])
			t, ok := byElement[eid]
			if !ok {
				t = &totals{sum: make(map[string]float64)}
				byElement[eid] = t
			}
			for _, f := range sumFields {
				t.sum[f] += num(row[f])
			}
			t.name = row["name"]
			t.pos = row["position"]
			t.team = row["team"]
			t.nowCost = numInt(row["value"]) // latest value wins
			if num(row["minutes"]) > 0 {
				t.played++
			}
			if gw >= windowStart {
				recentPoints[eid] = append(recentPoints[eid], num(row["total_points"]))
			}
		}
	}

	players := make([]fpl.Player, 0, len(byElement))
	for eid, t := range byElement {
		var form float64
		if pts := recentPoints[eid]; len(pts) > 0 {
			sum := 0.0
			for _, p := range pts {
				sum += p
			}
			form = sum / float64(len(pts))
		}
		var ppg float64
		if t.played > 0 {
			ppg = t.sum["total_points"] / float64(t.played)
		}

		players = append(players, fpl.Player{
			ID:            eid,
			Code:          eid,
			Team:          nameToID[t.team],
			ElementType:   positionToElementType[t.pos],
			WebName:       t.name,
			Status:        "a",
			TotalPoints:   int(t.sum["total_points"]),
			Form:          fpl.Num(round1(form)),
			PointsPerGame: fpl.Num(round1(ppg)),
			EPNext:        0, // not reconstructable — see package docs
			NowCost:       t.nowCost,
			Minutes:       int(t.sum["minutes"]),
			Starts:        int(t.sum["starts"]),
			Bonus:         int(t.sum["bonus"]),
			BPS:           int(t.sum["bps"]),
			CleanSheets:   int(t.sum["clean_sheets"]),
			YellowCards:   int(t.sum["yellow_cards"]),
			RedCards:      int(t.sum["red_cards"]),
			GoalsScored:   int(t.sum["goals_scored"]),
			Assists:       int(t.sum["assists"]),
			ICTIndex:      fpl.Num(round1(t.sum["ict_index"])),
			Influence:     fpl.Num(round1(t.sum["influence"])),
			Creativity:    fpl.Num(round1(t.sum["creativity"])),
			Threat:        fpl.Num(round1(t.sum["threat"])),

			ExpectedGoals:            fpl.Num(round2(t.sum["expected_goals"])),
			ExpectedAssists:          fpl.Num(round2(t.sum["expected_assists"])),
			ExpectedGoalInvolvements: fpl.Num(round2(t.sum["expected_goal_involvements"])),
		})
	}
	return players, nil
}

func round1(v float64) float64 { return round(v, 10) }
func round2(v float64) float64 { return round(v, 100) }
func round(v, mult float64) float64 {
	return float64(int(v*mult+0.5*sign(v))) / mult
}
func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

func (c *Corpus) actualResults(ctx context.Context, season string, gw int) (map[int]ActualEntry, error) {
	rows, err := c.csvRows(ctx, fmt.Sprintf("%s/gws/gw%d.csv", season, gw))
	if err != nil {
		return nil, fmt.Errorf("load gw%d actuals: %w", gw, err)
	}
	out := make(map[int]ActualEntry, len(rows))
	for _, r := range rows {
		eid := numInt(r["element"])
		out[eid] = ActualEntry{
			WebName:  r["name"],
			Team:     r["team"],
			Position: r["position"],
			Points:   numInt(r["total_points"]),
			Minutes:  numInt(r["minutes"]),
		}
	}
	return out, nil
}

// FuturePoints sums actual points and minutes each player accrued across
// [fromGW, toGW], keyed by FPL element id — the forward-looking counterpart
// to BuildCase's backward-looking reconstruction, for validating a signal
// against what players actually went on to score rather than backtesting a
// single gameweek's captain pick.
func (c *Corpus) FuturePoints(ctx context.Context, season string, fromGW, toGW int) (map[int]FuturePlayerPoints, error) {
	out := make(map[int]FuturePlayerPoints)
	for gw := fromGW; gw <= toGW; gw++ {
		rows, err := c.csvRows(ctx, fmt.Sprintf("%s/gws/gw%d.csv", season, gw))
		if err != nil {
			return nil, fmt.Errorf("load gw%d: %w", gw, err)
		}
		for _, r := range rows {
			eid := numInt(r["element"])
			p := out[eid]
			p.Points += numInt(r["total_points"])
			p.Minutes += numInt(r["minutes"])
			if numInt(r["minutes"]) > 0 {
				p.Appearances++
			}
			out[eid] = p
		}
	}
	return out, nil
}

// FuturePlayerPoints is one player's summed actual output over a gameweek
// range.
type FuturePlayerPoints struct {
	Points      int
	Minutes     int
	Appearances int
}

func buildTeamsJSON(byID map[int]teamRow) []fpl.Team {
	out := make([]fpl.Team, 0, len(byID))
	for id, r := range byID {
		strength := r.strength
		out = append(out, fpl.Team{
			ID: id, Code: r.code, Name: r.name, ShortName: r.shortName,
			Strength: &strength, Position: r.position, Played: r.played, Points: r.points,
			StrengthOverallHome: r.strengthOverallHome, StrengthOverallAway: r.strengthOverallAway,
			StrengthAttackHome: r.strengthAttackHome, StrengthAttackAway: r.strengthAttackAway,
			StrengthDefenceHome: r.strengthDefenceHome, StrengthDefenceAway: r.strengthDefenceAway,
		})
	}
	return out
}

// BuildCase reconstructs the point-in-time state a manager would have seen
// going into predictGW (state summed through predictGW-1), plus predictGW's
// actual results for scoring the algorithm's picks against reality.
// predictGW must be >= 2, since GW1 has no prior state to reconstruct from.
func (c *Corpus) BuildCase(ctx context.Context, season string, predictGW int) (*Case, error) {
	if predictGW < 2 {
		return nil, fmt.Errorf("predictGW must be >= 2 (no prior state before GW1), got %d", predictGW)
	}
	uptoGW := predictGW - 1

	teamsByID, nameToID, err := c.loadTeams(ctx, season)
	if err != nil {
		return nil, err
	}
	players, err := c.buildState(ctx, season, uptoGW, nameToID)
	if err != nil {
		return nil, err
	}
	fixtures, err := c.loadFixtures(ctx, season)
	if err != nil {
		return nil, err
	}
	actual, err := c.actualResults(ctx, season, predictGW)
	if err != nil {
		return nil, err
	}

	return &Case{
		Season:    season,
		PredictGW: predictGW,
		Bootstrap: &fpl.Bootstrap{Elements: players, Teams: buildTeamsJSON(teamsByID)},
		Fixtures:  fixtures,
		Actual:    actual,
	}, nil
}
