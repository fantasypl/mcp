package insights

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// ShortRestThresholdDays: a team playing again within this many days of its
// previous fixture, across every competition, is "congested" — squad
// rotation and fatigue risk rise sharply on a short turnaround. Three days
// (e.g. a Wednesday Champions League tie before a Saturday Premier League
// match) is the standard short-turnaround threshold in football fixture
// analysis, and the value `fplctl backtest -congestion` validated — see
// CHANGELOG.md.
const ShortRestThresholdDays = 3.0

// TeamFixtureCalendar fetches "By Gameweek" fixtures.csv for every gameweek
// in [fromGW, toGW] and returns each team's sorted match kickoff times
// across all competitions (Premier League, Champions League, Europa,
// Conference League, EFL Cup), keyed by FPL's stable team code — the only
// identifier a cross-competition fixtures.csv carries, since the opponent
// side is blank when it isn't an FPL-tracked club.
//
// "By Gameweek" (as opposed to "By Tournament/{competition}") already
// merges every competition's fixtures for a gameweek into one file,
// verified live, so this needs one request per gameweek rather than one per
// competition per gameweek. A gameweek this source doesn't cover is skipped
// rather than an error, the same convention GameweekFile's other callers
// use; if every gameweek in range is unavailable, returns ErrNotAvailable.
func (c *Client) TeamFixtureCalendar(ctx context.Context, season string, fromGW, toGW int) (map[int][]time.Time, error) {
	dates := make(map[int][]time.Time)
	found := false
	for gw := fromGW; gw <= toGW; gw++ {
		rows, err := c.GameweekFile(ctx, season, gw, "fixtures.csv")
		if errors.Is(err, ErrNotAvailable) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("GW%d fixtures: %w", gw, err)
		}
		found = true
		for _, row := range rows {
			kt, err := time.Parse(time.RFC3339, row["kickoff_time"])
			if err != nil {
				continue
			}
			for _, side := range []string{"home_team", "away_team"} {
				if row[side] == "" {
					continue
				}
				code := Int(row[side])
				if code == 0 {
					continue
				}
				dates[code] = append(dates[code], kt)
			}
		}
	}
	if !found {
		return nil, ErrNotAvailable
	}
	for code := range dates {
		sort.Slice(dates[code], func(i, j int) bool { return dates[code][i].Before(dates[code][j]) })
	}
	return dates, nil
}

// RestDaysBefore returns how many days before ref the latest date strictly
// earlier than ref falls, and false if there is no such date (no known
// prior fixture — e.g. the team's season-opening match). dates must already
// be sorted ascending, as TeamFixtureCalendar returns them.
func RestDaysBefore(dates []time.Time, ref time.Time) (float64, bool) {
	var prev time.Time
	found := false
	for _, d := range dates {
		if !d.Before(ref) {
			break
		}
		prev, found = d, true
	}
	if !found {
		return 0, false
	}
	return ref.Sub(prev).Hours() / 24, true
}
