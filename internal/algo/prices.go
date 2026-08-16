package algo

import (
	"context"
	"slices"
)

// Price predictions estimate which players will rise or fall in price tonight.
//
// FPL adjusts prices nightly on net transfer volume, but never publishes the
// threshold, so an exact prediction is not possible from the public API. What
// is available is each player's transfers in and out for the current gameweek,
// which ranks candidates reliably even when the absolute cutoff is unknown.
// Confidence below is therefore a relative estimate, not a probability.

// riseThreshold is a deliberately conservative stand-in for FPL's undisclosed
// cutoff: net transfers around this level have historically moved a price by
// £0.1m. It scales the confidence estimate and nothing else.
const riseThreshold = 500_000

const priceNote = "Price changes occur nightly. Confidence is a relative estimate " +
	"based on net transfer volume — not a guaranteed prediction."

type PriceResult struct {
	Note          string      `json:"note"`
	LikelyRisers  []PriceMove `json:"likely_risers"`
	LikelyFallers []PriceMove `json:"likely_fallers"`
}

type PriceMove struct {
	Player         PriceBrief `json:"player"`
	NetTransfersGW int        `json:"net_transfers_gw"`
	TransfersInGW  int        `json:"transfers_in_gw"`
	TransfersOutGW int        `json:"transfers_out_gw"`
	Direction      string     `json:"direction"`
	Confidence     int        `json:"confidence"`
}

type PriceBrief struct {
	ID              int     `json:"id"`
	Name            string  `json:"name"`
	Team            string  `json:"team"`
	Position        string  `json:"position"`
	CurrentPrice    float64 `json:"current_price"`
	ChangeFromStart float64 `json:"change_from_start"`
	SelectedByPct   float64 `json:"selected_by_pct"`
}

// PricePredictions ranks players by net transfer activity this gameweek.
// topN defaults to 20.
func (e *Engine) PricePredictions(ctx context.Context, topN int) (*PriceResult, error) {
	if topN <= 0 {
		topN = 20
	}

	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	teams := teamsByID(bootstrap)

	var risers, fallers []PriceMove

	for i := range bootstrap.Elements {
		p := &bootstrap.Elements[i]
		if InjuryStatuses[p.Status] {
			continue
		}

		net := p.TransfersInEvent - p.TransfersOutEvent
		if net == 0 {
			continue
		}

		move := PriceMove{
			Player: PriceBrief{
				ID:              p.ID,
				Name:            p.WebName,
				Team:            shortName(teams[p.Team]),
				Position:        Position(p.ElementType),
				CurrentPrice:    float64(p.NowCost) / 10,
				ChangeFromStart: float64(p.CostChangeStart) / 10,
				SelectedByPct:   p.SelectedByPercent.Float(),
			},
			NetTransfersGW: net,
			TransfersInGW:  p.TransfersInEvent,
			TransfersOutGW: p.TransfersOutEvent,
		}

		if net > 0 {
			move.Direction = "rise"
			move.Confidence = min(100, RoundToInt(float64(net)/riseThreshold*100))
			risers = append(risers, move)
		} else {
			move.Direction = "fall"
			move.Confidence = min(100, RoundToInt(float64(-net)/riseThreshold*100))
			fallers = append(fallers, move)
		}
	}

	// Risers descend from the largest net gain, fallers ascend from the
	// largest net loss, so both lists lead with the most likely to move.
	slices.SortStableFunc(risers, func(a, b PriceMove) int { return b.NetTransfersGW - a.NetTransfersGW })
	slices.SortStableFunc(fallers, func(a, b PriceMove) int { return a.NetTransfersGW - b.NetTransfersGW })

	return &PriceResult{
		Note:          priceNote,
		LikelyRisers:  capMoves(risers, topN),
		LikelyFallers: capMoves(fallers, topN),
	}, nil
}

func capMoves(m []PriceMove, n int) []PriceMove {
	if m == nil {
		return []PriceMove{}
	}
	if len(m) > n {
		return m[:n]
	}
	return m
}
