package fpl

// PlayerSummary is GET /element-summary/{player_id}/ — one player's match
// history and upcoming fixtures.
//
// The real payload carries a "fixtures" array alongside "history", plus
// dozens of per-match stat fields (goals, assists, ict_index, bps, and so
// on). Only "history" and, within it, was_home/minutes/total_points are
// modelled here: the only algorithm that calls this endpoint so far reads
// nothing else — see compare.go's _calc_home_away_form calculation. Add
// fields here only when an algorithm actually reads them, per this package's
// field-selection convention (see the doc comment atop types.go).
type PlayerSummary struct {
	History []PlayerHistoryEntry `json:"history"`
}

// PlayerHistoryEntry is one played match from a player's history.
type PlayerHistoryEntry struct {
	WasHome     bool `json:"was_home"`
	Minutes     int  `json:"minutes"`
	TotalPoints int  `json:"total_points"`
}
