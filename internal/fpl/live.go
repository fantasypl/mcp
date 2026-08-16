package fpl

// LiveResponse is GET /event/{gw}/live/ — real-time stats for every player
// during an active gameweek, refreshed roughly every couple of minutes while
// matches are in progress. Also the shape of the cached
// data/backtest_cache/live_gw{N}.json files the weight optimizer replays.
//
// Unlike the bootstrap payload, live stats are plain JSON numbers rather than
// FPL's string-encoded quirks — nothing here needs fpl.Num.
type LiveResponse struct {
	Elements []LiveElement `json:"elements"`
}

// LiveElement is one player's live gameweek stats.
type LiveElement struct {
	ID    int       `json:"id"`
	Stats LiveStats `json:"stats"`
}

// LiveStats holds the subset of live per-player stats the algorithms read:
// minutes played, points so far, and the BPS/bonus figures used to project or
// confirm bonus points.
type LiveStats struct {
	Minutes     int `json:"minutes"`
	TotalPoints int `json:"total_points"`
	BPS         int `json:"bps"`
	Bonus       int `json:"bonus"`
}

// ActualPoints indexes live results by player ID. Used both by the live
// scoring algorithm and by the weight optimizer's backtest, which is why it
// lives on the shared type rather than being duplicated per caller.
func (l LiveResponse) ActualPoints() map[int]int {
	out := make(map[int]int, len(l.Elements))
	for _, e := range l.Elements {
		out[e.ID] = e.Stats.TotalPoints
	}
	return out
}

// ByID indexes live elements by player ID for O(1) lookup during enrichment.
func (l LiveResponse) ByID() map[int]LiveElement {
	out := make(map[int]LiveElement, len(l.Elements))
	for _, e := range l.Elements {
		out[e.ID] = e
	}
	return out
}

// EventStatusResponse is GET /event-status/ — whether bonus points for the
// last few days' matches are confirmed or still provisional.
type EventStatusResponse struct {
	Status  []EventStatusDay `json:"status"`
	Leagues string           `json:"leagues"`
}

type EventStatusDay struct {
	Date       string `json:"date"`
	BonusAdded bool   `json:"bonus_added"`
	Points     string `json:"points"`
}

// BonusConfirmed reports whether every returned day has bonus_added=true.
// Bonus is only "confirmed" once every day in the window
// agrees, and an empty status list (no matches played yet) is not confirmed.
func (e EventStatusResponse) BonusConfirmed() bool {
	if len(e.Status) == 0 {
		return false
	}
	for _, d := range e.Status {
		if !d.BonusAdded {
			return false
		}
	}
	return true
}
