package fpl

// LeagueStandings is GET /leagues-classic/{league_id}/standings/ — a classic
// mini-league's table: who's in it, their rank, and their season total.
type LeagueStandings struct {
	League    LeagueInfo    `json:"league"`
	Standings StandingsPage `json:"standings"`
}

type LeagueInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type StandingsPage struct {
	Results []LeagueEntry `json:"results"`
}

// LeagueEntry is one manager's row in the standings table.
type LeagueEntry struct {
	Entry      int    `json:"entry"`
	EntryName  string `json:"entry_name"`
	PlayerName string `json:"player_name"`
	Rank       int    `json:"rank"`
	Total      int    `json:"total"`
	EventTotal int    `json:"event_total"`
}

// TeamHistory is GET /entry/{team_id}/history/ — season history, past
// gameweek scores, and chips used.
type TeamHistory struct {
	Current []HistoryGameweek `json:"current"`
	Chips   []ChipUsage       `json:"chips"`
}

// HistoryGameweek is one gameweek's result within the current season.
type HistoryGameweek struct {
	Event  int `json:"event"`
	Points int `json:"points"`
}

// ChipUsage is one chip play recorded in a manager's history.
type ChipUsage struct {
	Name  string `json:"name"`
	Event int    `json:"event"`
}

// ManagerTransfer is one entry of GET /entry/{team_id}/transfers/ — a single
// transfer a manager made this season, most recent first.
type ManagerTransfer struct {
	Event          int `json:"event"`
	ElementIn      int `json:"element_in"`
	ElementInCost  int `json:"element_in_cost"`
	ElementOut     int `json:"element_out"`
	ElementOutCost int `json:"element_out_cost"`
}
