package fpl

// TeamPicks is GET /entry/{team_id}/event/{gw}/picks/ — a manager's squad for
// one gameweek: which 15 players, who's captain, and whether a chip is active.
//
// This is fundamentally different from Bootstrap or Fixtures: those are
// global season data that FPL, and several third parties, keep observing and
// republishing. A manager's picks are generated once, by FPL's own backend,
// when that manager submits a team — nobody else ever has this data, and FPL
// itself does not archive it past the season it belongs to. Gameweek numbers
// 1-38 are reused every season with no season parameter anywhere in the API,
// so once a season rolls over, its picks are gone from every source,
// official or third-party, permanently. Algorithms that need this data are
// tested without real historical picks.
type TeamPicks struct {
	Picks        []Pick       `json:"picks"`
	ActiveChip   *string      `json:"active_chip"`
	EntryHistory EntryHistory `json:"entry_history"`
}

// Pick is one squad slot. Position runs 1-15 (1-11 are the starting XI,
// 12-15 the bench, ordered GKP-first within the bench). Multiplier is what
// FPL actually uses to compute points from this pick: 0 for a benched player,
// 1 for a normal starter, 2 for the captain, 3 under triple captain.
type Pick struct {
	Element       int  `json:"element"`
	Position      int  `json:"position"`
	Multiplier    int  `json:"multiplier"`
	IsCaptain     bool `json:"is_captain"`
	IsViceCaptain bool `json:"is_vice_captain"`
}

// EntryHistory is the picks response's per-gameweek summary for the manager:
// bank balance, transfers made that gameweek, and season standing so far.
type EntryHistory struct {
	Bank           int `json:"bank"`
	EventTransfers int `json:"event_transfers"`
	OverallRank    int `json:"overall_rank"`
	TotalPoints    int `json:"total_points"`
	PointsOnBench  int `json:"points_on_bench"`
}

// Starters returns the picks with Position <= 11 — the starting XI, excluding
// bench.
func (t TeamPicks) Starters() []Pick {
	out := make([]Pick, 0, 11)
	for _, p := range t.Picks {
		if p.Position <= 11 {
			out = append(out, p)
		}
	}
	return out
}

// Bench returns the four bench picks, in bench order (12-15).
func (t TeamPicks) Bench() []Pick {
	out := make([]Pick, 0, 4)
	for _, p := range t.Picks {
		if p.Position > 11 {
			out = append(out, p)
		}
	}
	return out
}

// CaptainElement returns the captain's player ID, or 0 if none is marked —
// which should not happen for a valid squad but is not this type's job to
// enforce.
func (t TeamPicks) CaptainElement() int {
	for _, p := range t.Picks {
		if p.IsCaptain {
			return p.Element
		}
	}
	return 0
}
