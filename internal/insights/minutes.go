package insights

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// PlayerMinutes is one player's playing time across a window of Premier
// League matches, from playermatchstats.csv's minutes_played column.
//
// minutes_played is reliable where the same file's start_min/finish_min
// columns are not: verified live (see the minutes-model measurement in
// CHANGELOG.md), start_min/finish_min are essentially unpopulated, but
// minutes_played is well-distributed across the full 1-90 range for every
// player who actually appeared.
type PlayerMinutes struct {
	PlayerID   int
	SumMinutes int
	Matches    int
}

// AvgMinutes is the mean minutes played per Matches appearance, or 0 if
// there are none. A low AvgMinutes means most of a player's appearances in
// the window were substitute cameos rather than starts — a useful gate for
// any signal (like positional drift) that a brief cameo can skew without
// reflecting a sustained change.
func (m PlayerMinutes) AvgMinutes() float64 {
	if m.Matches == 0 {
		return 0
	}
	return float64(m.SumMinutes) / float64(m.Matches)
}

// PlayerMinutesInRange fetches playermatchstats.csv for every gameweek in
// [fromGW, toGW] and sums each Premier League player's minutes played
// across that span, keyed by FPL player id. Premier League matches are
// identified by match_id's "-prem-" segment, matching FinishingLuck's and
// AveragePositions' filter. A missing gameweek is skipped rather than an
// error; if every gameweek in range is unavailable, returns
// ErrNotAvailable.
func (c *Client) PlayerMinutesInRange(ctx context.Context, season string, fromGW, toGW int) (map[int]PlayerMinutes, error) {
	out := make(map[int]PlayerMinutes)
	found := false
	for gw := fromGW; gw <= toGW; gw++ {
		rows, err := c.GameweekFile(ctx, season, gw, "playermatchstats.csv")
		if errors.Is(err, ErrNotAvailable) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("GW%d playermatchstats: %w", gw, err)
		}
		found = true
		for _, row := range rows {
			if !strings.Contains(row["match_id"], "-prem-") {
				continue // European/cup fixture — out of scope for this signal
			}
			id := Int(row["player_id"])
			if id == 0 {
				continue
			}
			pm := out[id]
			pm.PlayerID = id
			pm.SumMinutes += Int(row["minutes_played"])
			pm.Matches++
			out[id] = pm
		}
	}
	if !found {
		return nil, ErrNotAvailable
	}
	return out, nil
}
