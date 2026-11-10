package algo

import (
	"context"
	"slices"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/fantasypl/mcp/internal/fpl"
)

// Transfer return on investment for manager_transfer_history (issue #50).
//
// For each transfer made in gameweek E, the incoming player is held from E
// until the gameweek F in which he is sold, or through the last finished
// gameweek if he is still owned. Over those gameweeks:
//
//	points_since_in  = the incoming player's points
//	points_since_out = the outgoing player's points
//	net_gain         = points_since_in - points_since_out - hit_cost
//
// Points are each player's own gameweek scores from /event/{gw}/live/,
// whether or not he started: captaincy, the bench and automatic
// substitutions are ignored, so this measures the swap, not the line-up.
//
// hit_cost: FPL spends free transfers first, so a gameweek's hit falls on
// its last transfers by time, 4 points each. Transfers confirmed together
// share a timestamp to the second but are ordered by microseconds, so the
// attribution within one batch follows the order FPL recorded and is a
// convention more than a fact. by_gameweek rolls each gameweek's transfers
// up together, which needs no attribution.

const transferROINote = "points_since_in and points_since_out are each player's own points over the gameweeks the incoming player was held, whether or not he started; captaincy and the bench are ignored. net_gain subtracts hit_cost. A gameweek's hit is charged to its last transfers by time, because FPL uses free transfers first; transfers confirmed together are ordered as FPL recorded them, so by_gameweek, which adds up a gameweek's transfers together, is the more robust view."

// pointsPerHit is the cost of one extra transfer: 4, the negation of
// hit_analyzer.go's hitCost.
const pointsPerHit = -hitCost

// reversalWindowGW is the issue's threshold for a hit "reversed" soon after:
// the incoming player sold within this many gameweeks.
const reversalWindowGW = 5

// TransferROIGameweek rolls up one gameweek's transfers.
type TransferROIGameweek struct {
	Gameweek     int    `json:"gameweek"`
	Transfers    int    `json:"transfers"`
	Chip         string `json:"chip,omitempty"`
	PointsGained int    `json:"points_gained"` // sum of points_since_in - points_since_out
	HitCost      int    `json:"hit_cost"`
	NetGain      int    `json:"net_gain"`
}

// TransferROISummary totals every transfer's return.
type TransferROISummary struct {
	ThroughGameweek       int                   `json:"through_gameweek"` // last finished gameweek counted
	Hits                  int                   `json:"hits"`
	TotalHitCost          int                   `json:"total_hit_cost"`
	TotalNetGain          int                   `json:"total_net_gain"`
	HitsReversedWithin5GW int                   `json:"hits_reversed_within_5_gw"` // hit transfers whose incoming player was sold within 5 gameweeks
	ByGameweek            []TransferROIGameweek `json:"by_gameweek"`
	Note                  string                `json:"note"`
}

// transferROI is one transfer's return.
type transferROI struct {
	pointsIn, pointsOut, hitCost, held int
	soldIn                             *int
	soldByWildcard                     bool
}

// transferKey identifies a transfer across orderings.
type transferKey struct {
	event, in, out int
	time           string
}

func keyOf(t fpl.ManagerTransfer) transferKey {
	return transferKey{event: t.Event, in: t.ElementIn, out: t.ElementOut, time: t.Time}
}

// attributeHits returns each transfer's share of its gameweek's hit cost,
// in the order given. costByGW maps gameweek to that gameweek's total cost.
func attributeHits(transfers []fpl.ManagerTransfer, costByGW map[int]int) []int {
	out := make([]int, len(transfers))
	byGW := make(map[int][]int)
	for i, t := range transfers {
		byGW[t.Event] = append(byGW[t.Event], i)
	}
	for gw, idx := range byGW {
		hits := costByGW[gw] / pointsPerHit
		if hits <= 0 {
			continue
		}
		// Latest first; the last `hits` transfers carry 4 points each.
		slices.SortStableFunc(idx, func(a, b int) int {
			switch {
			case transfers[a].Time > transfers[b].Time:
				return -1
			case transfers[a].Time < transfers[b].Time:
				return 1
			default:
				return 0
			}
		})
		for k := 0; k < hits && k < len(idx); k++ {
			out[idx[k]] = pointsPerHit
		}
	}
	return out
}

// computeTransferROI works out every transfer's return through gameweek
// lastGW. points[gw][id] is a player's points in gameweek gw.
func computeTransferROI(transfers []fpl.ManagerTransfer, history *fpl.TeamHistory, points map[int]map[int]int, lastGW int) (map[transferKey]transferROI, *TransferROISummary) {
	chrono := slices.Clone(transfers)
	slices.SortStableFunc(chrono, func(a, b fpl.ManagerTransfer) int {
		switch {
		case a.Event != b.Event:
			return a.Event - b.Event
		case a.Time < b.Time:
			return -1
		case a.Time > b.Time:
			return 1
		default:
			return 0
		}
	})

	costByGW := make(map[int]int)
	for _, g := range history.Current {
		costByGW[g.Event] = g.EventTransfersCost
	}
	chipByGW := make(map[int]string)
	for _, c := range history.Chips {
		chipByGW[c.Event] = c.Name
	}
	hits := attributeHits(chrono, costByGW)

	out := make(map[transferKey]transferROI, len(chrono))
	rollup := make(map[int]*TransferROIGameweek)
	sum := &TransferROISummary{ThroughGameweek: lastGW, Note: transferROINote}
	for i, t := range chrono {
		// The incoming player is held until the next transfer that sells him.
		end := lastGW + 1
		var soldIn *int
		for _, later := range chrono[i+1:] {
			if later.ElementOut == t.ElementIn {
				end = later.Event
				soldIn = ptr(later.Event)
				break
			}
		}
		r := transferROI{hitCost: hits[i], soldIn: soldIn, held: max(0, end-t.Event)}
		for gw := t.Event; gw < end; gw++ {
			r.pointsIn += points[gw][t.ElementIn]
			r.pointsOut += points[gw][t.ElementOut]
		}
		if soldIn != nil && chipByGW[*soldIn] == "wildcard" {
			r.soldByWildcard = true
		}
		out[keyOf(t)] = r

		net := r.pointsIn - r.pointsOut - r.hitCost
		sum.TotalNetGain += net
		if r.hitCost > 0 && soldIn != nil && r.held <= reversalWindowGW {
			sum.HitsReversedWithin5GW++
		}
		g := rollup[t.Event]
		if g == nil {
			g = &TransferROIGameweek{Gameweek: t.Event, Chip: chipByGW[t.Event], HitCost: costByGW[t.Event]}
			rollup[t.Event] = g
		}
		g.Transfers++
		g.PointsGained += r.pointsIn - r.pointsOut
	}

	for _, g := range rollup {
		g.NetGain = g.PointsGained - g.HitCost
		sum.TotalHitCost += g.HitCost
		sum.ByGameweek = append(sum.ByGameweek, *g)
	}
	slices.SortFunc(sum.ByGameweek, func(a, b TransferROIGameweek) int { return a.Gameweek - b.Gameweek })
	sum.Hits = sum.TotalHitCost / pointsPerHit
	if sum.ByGameweek == nil {
		sum.ByGameweek = []TransferROIGameweek{}
	}
	return out, sum
}

// gameweekPoints fetches each gameweek's live points from first to last,
// six at a time. It returns false if any gameweek can't be fetched, since a
// gap would understate every transfer spanning it.
func (e *Engine) gameweekPoints(ctx context.Context, first, last int) (map[int]map[int]int, bool) {
	var mu sync.Mutex
	out := make(map[int]map[int]int, max(0, last-first+1))
	ok := true
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(6)
	for gw := first; gw <= last; gw++ {
		g.Go(func() error {
			lv, err := e.client.LivePoints(gctx, gw)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				ok = false
				return nil
			}
			out[gw] = lv.ActualPoints()
			return nil
		})
	}
	_ = g.Wait() // every goroutine returns nil
	return out, ok
}

// lastFinishedGameweek is the highest finished gameweek, or 0.
func lastFinishedGameweek(b *fpl.Bootstrap) int {
	last := 0
	for _, ev := range b.Events {
		if ev.Finished && ev.ID > last {
			last = ev.ID
		}
	}
	return last
}

// addTransferROI fills in each entry's return and the ROI summary.
func (e *Engine) addTransferROI(ctx context.Context, result *TransferHistoryResult, transfers []fpl.ManagerTransfer, history *fpl.TeamHistory, historyErr error, bootstrap *fpl.Bootstrap) {
	if len(transfers) == 0 {
		return
	}
	if historyErr != nil {
		result.ROINote = "Could not fetch the season history, so hit costs and returns are not shown."
		return
	}
	first := transfers[0].Event
	for _, t := range transfers {
		first = min(first, t.Event)
	}
	last := lastFinishedGameweek(bootstrap)
	points, ok := e.gameweekPoints(ctx, first, last)
	if !ok {
		result.ROINote = "Could not fetch every gameweek's live points, so returns are not shown."
		return
	}

	perTransfer, summary := computeTransferROI(transfers, history, points, last)
	for i := range result.Transfers {
		en := &result.Transfers[i]
		r := perTransfer[transferKey{event: en.Gameweek, in: en.PlayerIn.ID, out: en.PlayerOut.ID, time: en.Time}]
		en.PointsSinceIn, en.PointsSinceOut, en.HitCost = r.pointsIn, r.pointsOut, r.hitCost
		en.NetGain = r.pointsIn - r.pointsOut - r.hitCost
		en.HeldGameweeks, en.SoldInGameweek, en.SoldByWildcard = r.held, r.soldIn, r.soldByWildcard
	}
	result.ROI = summary
}
