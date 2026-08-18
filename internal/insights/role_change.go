package insights

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// minMatchesForPositionDrift is the minimum Premier League appearances (with
// average_positions.csv coverage) a player needs in *each* window before a
// positional-drift figure is trusted — below this the average is too noisy
// to mean anything.
const minMatchesForPositionDrift = 3

// PlayerPosition is one player's average pitch position, aggregated over a
// window of Premier League matches. x is advancement — 0 at the player's
// own goal-line, 100 at the opponent's, already normalized to each team's
// own attacking direction (verified live: a home team's goalkeeper and an
// away team's goalkeeper both average x≈9, not one near 0 and the other
// near 100) — so a higher x means a more advanced average position,
// comparable directly across players regardless of home/away. y is width
// (0-100) and is tracked but not otherwise used yet.
type PlayerPosition struct {
	PlayerID int
	Name     string
	// Position is average_positions.csv's own position column (G/D/M/F) —
	// whichever value was seen last across the window's matches. A player's
	// listed position is effectively constant match to match, so this is
	// for filtering (e.g. excluding goalkeepers, whose position is
	// structurally near-static and not a meaningful "stable role" baseline
	// to compare drift against), not itself a signal.
	Position string
	SumX     float64
	SumY     float64
	Matches  int
}

// AvgX is the mean advancement across Matches, or 0 if there are none.
func (p PlayerPosition) AvgX() float64 {
	if p.Matches == 0 {
		return 0
	}
	return p.SumX / float64(p.Matches)
}

// AvgY is the mean width across Matches, or 0 if there are none.
func (p PlayerPosition) AvgY() float64 {
	if p.Matches == 0 {
		return 0
	}
	return p.SumY / float64(p.Matches)
}

// AveragePositions fetches average_positions.csv for every gameweek in
// [fromGW, toGW] and aggregates each Premier League player's average pitch
// position across that span, keyed by FPL player id. Premier League matches
// are identified by match_id's "-prem-" segment — the FPL-Core-Insights
// match-id convention, matching FinishingLuck's filter — since
// average_positions.csv itself carries no competition column. A missing
// gameweek is skipped rather than an error, the same convention
// GameweekFile's other callers use; if every gameweek in range is
// unavailable, returns ErrNotAvailable.
func (c *Client) AveragePositions(ctx context.Context, season string, fromGW, toGW int) (map[int]PlayerPosition, error) {
	out := make(map[int]PlayerPosition)
	found := false
	for gw := fromGW; gw <= toGW; gw++ {
		rows, err := c.GameweekFile(ctx, season, gw, "average_positions.csv")
		if errors.Is(err, ErrNotAvailable) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("GW%d average_positions: %w", gw, err)
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
			pp := out[id]
			pp.PlayerID = id
			pp.Name = row["player_name"]
			pp.Position = row["position"]
			pp.SumX += Float(row["x"])
			pp.SumY += Float(row["y"])
			pp.Matches++
			out[id] = pp
		}
	}
	if !found {
		return nil, ErrNotAvailable
	}
	return out, nil
}

// PositionDrift is how much a player's average pitch position has advanced
// (positive DeltaX) or dropped (negative) between an earlier baseline
// window and a more recent one — the role-change signal: a player pushed
// into a more advanced role should show a positive drift before goals,
// assists, and other box-score stats catch up.
type PositionDrift struct {
	PlayerID int
	Name     string
	// Position is the recent window's PlayerPosition.Position — the
	// player's current listed position, for filtering.
	Position                       string
	BaselineX, RecentX             float64
	BaselineMatches, RecentMatches int
}

// DeltaX is RecentX minus BaselineX — positive means the player is
// averaging a more advanced position recently than in the baseline window.
func (d PositionDrift) DeltaX() float64 { return d.RecentX - d.BaselineX }

// Qualified reports whether d has enough matches in both windows for DeltaX
// to be meaningful.
func (d PositionDrift) Qualified() bool {
	return d.BaselineMatches >= minMatchesForPositionDrift && d.RecentMatches >= minMatchesForPositionDrift
}

// ComputePositionDrift pairs each player present in both baseline and
// recent (two AveragePositions results over non-overlapping windows) into a
// PositionDrift. A player missing from either window — no baseline
// appearances, or none recently — is excluded rather than treated as a
// drift from/to zero, since that would conflate "moved position" with
// "wasn't playing."
func ComputePositionDrift(baseline, recent map[int]PlayerPosition) map[int]PositionDrift {
	out := make(map[int]PositionDrift, len(baseline))
	for id, b := range baseline {
		r, ok := recent[id]
		if !ok {
			continue
		}
		out[id] = PositionDrift{
			PlayerID:        id,
			Name:            r.Name,
			Position:        r.Position,
			BaselineX:       b.AvgX(),
			RecentX:         r.AvgX(),
			BaselineMatches: b.Matches,
			RecentMatches:   r.Matches,
		}
	}
	return out
}
