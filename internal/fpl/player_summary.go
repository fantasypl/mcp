package fpl

// PlayerSummary is GET /element-summary/{player_id}/ — one player's match
// history and upcoming fixtures.
//
// The real payload carries a "fixtures" array alongside "history", plus
// dozens of per-match stat fields (goals, assists, ict_index, bps, and so
// on). Only "history" and the per-match fields player_comparison reads are
// modelled here: was_home/minutes/total_points for its home/away split, and
// the fields listed on PlayerHistoryEntry for include_gameweeks. Add
// fields here only when an algorithm actually reads them, per this package's
// field-selection convention (see the doc comment atop types.go).
type PlayerSummary struct {
	History []PlayerHistoryEntry `json:"history"`
}

// PlayerHistoryEntry is one played match from a player's history.
//
// Round, OpponentTeam and the per-match stats feed player_comparison's
// include_gameweeks (issue #25). Each was checked against the live payload
// for 2026-27; xG and xA arrive as strings ("0.45"), hence Num.
type PlayerHistoryEntry struct {
	WasHome     bool `json:"was_home"`
	Minutes     int  `json:"minutes"`
	TotalPoints int  `json:"total_points"`

	Round           int `json:"round"`
	OpponentTeam    int `json:"opponent_team"`
	GoalsScored     int `json:"goals_scored"`
	Assists         int `json:"assists"`
	Bonus           int `json:"bonus"`
	ExpectedGoals   Num `json:"expected_goals"`
	ExpectedAssists Num `json:"expected_assists"`
}
