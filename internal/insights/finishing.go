package insights

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// minShotsOnTarget is the minimum xGOT-populated shots a player needs
// before their FinishingDelta figure is trusted — below this, the sample is
// too small to say anything.
const minShotsOnTarget = 5

// FinishingDelta is one player's shot-execution over/underperformance:
// actual goals from on-target shots minus the shot model's expected goals
// on target (xGOT) for those same shots.
//
// This isolates finishing luck from chance creation, which aggregate
// expected_goals conflates: a player can have poor aggregate xG because
// their chances are genuinely bad, because good chances are going off
// target, or because good on-target shots are being saved — only
// shot-level xG-vs-xGOT tells these apart. A positive Delta means
// outperforming shot quality (a "sell" signal — due to regress down); a
// negative Delta means underperforming it (a "buy" signal — due to regress
// up). Qualified reports whether ShotsOnTarget meets minShotsOnTarget —
// callers should not trust Delta otherwise.
type FinishingDelta struct {
	PlayerID      int
	Name          string
	ActualGoals   int
	SumXGOT       float64
	ShotsOnTarget int
}

// Delta is ActualGoals minus SumXGOT — see the type doc for interpretation.
func (f FinishingDelta) Delta() float64 { return float64(f.ActualGoals) - f.SumXGOT }

// Qualified reports whether f has enough on-target shots for Delta to be
// meaningful.
func (f FinishingDelta) Qualified() bool { return f.ShotsOnTarget >= minShotsOnTarget }

// FinishingLuck fetches shots.csv for every gameweek in [fromGW, toGW] and
// aggregates each Premier League player's finishing luck across that span,
// keyed by FPL player id. Premier League shots are identified by match_id's
// "-prem-" segment — the FPL-Core-Insights match-id convention, verified
// live — since shots.csv itself carries no competition column. A missing
// gameweek (not yet played, or a season shots.csv doesn't cover) is skipped
// rather than an error, the same convention GameweekFile's callers use
// elsewhere; if every gameweek in the range is unavailable, returns
// ErrNotAvailable.
func (c *Client) FinishingLuck(ctx context.Context, season string, fromGW, toGW int) (map[int]FinishingDelta, error) {
	out := make(map[int]FinishingDelta)
	found := false
	for gw := fromGW; gw <= toGW; gw++ {
		rows, err := c.GameweekFile(ctx, season, gw, "shots.csv")
		if errors.Is(err, ErrNotAvailable) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("GW%d shots: %w", gw, err)
		}
		found = true
		for _, row := range rows {
			if !strings.Contains(row["match_id"], "-prem-") {
				continue // European/cup shot — out of scope for this signal
			}
			if row["xgot"] == "" {
				continue // not on target, or xGOT genuinely unavailable for this shot
			}
			id := Int(row["player_id"])
			if id == 0 {
				continue
			}
			fl := out[id]
			fl.PlayerID = id
			fl.Name = row["player_name"]
			fl.SumXGOT += Float(row["xgot"])
			fl.ShotsOnTarget++
			if row["outcome"] == "goal" {
				fl.ActualGoals++
			}
			out[id] = fl
		}
	}
	if !found {
		return nil, ErrNotAvailable
	}
	return out, nil
}
