// Package fpl models the public Fantasy Premier League API and fetches from it.
//
// Field selection is deliberate rather than exhaustive: the bootstrap element
// object carries 105 fields, of which the algorithms read 47. Modelling only
// what is used keeps the struct reviewable, and an unmapped field is inert —
// encoding/json ignores it.
package fpl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Num is a float64 that tolerates how inconsistently FPL encodes numbers.
//
// The API returns the same *kind* of quantity in three different shapes:
//
//	"form":                  "6.2" // string
//	"expected_goals_per_90": 0.78 // number
//	"ep_this":                null // absent
//
// Handling this once here — rather than at every call site — means the rest
// of the codebase can treat every numeric FPL field as a plain float64,
// string-or-number-or-null included.
//
// Note the asymmetry with nullable fields: Num treats null as 0 because for
// form, xG and friends "no value" genuinely means zero. Fields where null
// carries a distinct meaning — chance_of_playing_next_round, where null means
// *fit* and 0 means *definitely out* — must use a pointer type instead.
type Num float64

func (n *Num) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*n = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" || s == "None" {
			*n = 0
			return nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("fpl.Num: %q is not numeric: %w", s, err)
		}
		*n = Num(f)
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("fpl.Num: %s: %w", b, err)
	}
	*n = Num(f)
	return nil
}

func (n Num) Float() float64 { return float64(n) }

// Bootstrap is GET /bootstrap-static/ — the ~1.3 MB payload carrying every
// player, team, gameweek, and scoring rule.
type Bootstrap struct {
	Elements     []Player      `json:"elements"`
	Teams        []Team        `json:"teams"`
	Events       []Event       `json:"events"`
	ElementTypes []ElementType `json:"element_types"`
}

// Player is one entry of bootstrap.elements.
type Player struct {
	ID          int    `json:"id"`
	Code        int    `json:"code"`
	Team        int    `json:"team"`
	ElementType int    `json:"element_type"`
	WebName     string `json:"web_name"`
	FirstName   string `json:"first_name"`
	SecondName  string `json:"second_name"`

	// Availability. Status is one of a/d/i/s/u/n; see algo.InjuryStatuses.
	Status string `json:"status"`
	News   string `json:"news"`
	// NewsAdded is null when there is no news.
	NewsAdded *string `json:"news_added"`
	// ChanceOfPlaying{Next,This}Round: null means fit, 0 means out. The
	// distinction is load-bearing — collapsing null to 0 inverts the injury
	// penalty — so these stay pointers.
	ChanceOfPlayingNextRound *int `json:"chance_of_playing_next_round"`
	ChanceOfPlayingThisRound *int `json:"chance_of_playing_this_round"`

	// Scoring and form. FPL sends these as strings.
	TotalPoints   int `json:"total_points"`
	EventPoints   int `json:"event_points"`
	Form          Num `json:"form"`
	PointsPerGame Num `json:"points_per_game"`
	EPNext        Num `json:"ep_next"`
	EPThis        Num `json:"ep_this"`
	ValueForm     Num `json:"value_form"`
	ValueSeason   Num `json:"value_season"`

	// Price and ownership.
	NowCost           int `json:"now_cost"`
	CostChangeEvent   int `json:"cost_change_event"`
	CostChangeStart   int `json:"cost_change_start"`
	SelectedByPercent Num `json:"selected_by_percent"`
	TransfersInEvent  int `json:"transfers_in_event"`
	TransfersOutEvent int `json:"transfers_out_event"`

	// Match involvement.
	Minutes        int `json:"minutes"`
	Starts         int `json:"starts"`
	Bonus          int `json:"bonus"`
	BPS            int `json:"bps"`
	CleanSheets    int `json:"clean_sheets"`
	YellowCards    int `json:"yellow_cards"`
	RedCards       int `json:"red_cards"`
	DreamteamCount int `json:"dreamteam_count"`
	// GoalsScored and Assists are season totals; like ExpectedGoalsConceded
	// above, only fplctl's snapshot capture reads these — no scoring
	// algorithm uses them (they use the expected-stat and per-90 fields).
	GoalsScored int `json:"goals_scored"`
	Assists     int `json:"assists"`

	// ICT. Strings in the payload; the *_rank fields are plain ints.
	ICTIndex       Num `json:"ict_index"`
	ICTIndexRank   int `json:"ict_index_rank"`
	Influence      Num `json:"influence"`
	InfluenceRank  int `json:"influence_rank"`
	Creativity     Num `json:"creativity"`
	CreativityRank int `json:"creativity_rank"`
	Threat         Num `json:"threat"`
	ThreatRank     int `json:"threat_rank"`

	// Expected stats. Season totals arrive as strings, per-90 as numbers.
	ExpectedGoals            Num `json:"expected_goals"`
	ExpectedAssists          Num `json:"expected_assists"`
	ExpectedGoalInvolvements Num `json:"expected_goal_involvements"`
	// ExpectedGoalsConceded is the season total; only fplctl's snapshot
	// capture reads it (as part of the snapshot field set) — no scoring
	// algorithm uses the season total, only the per-90 rate below.
	ExpectedGoalsConceded Num `json:"expected_goals_conceded"`

	CleanSheetsPer90           Num `json:"clean_sheets_per_90"`
	DefensiveContributionPer90 Num `json:"defensive_contribution_per_90"`
	ExpectedGoalsConcededPer90 Num `json:"expected_goals_conceded_per_90"`

	// Set-piece duties. Null means "not on them"; 1 is first choice, so the
	// null-vs-zero distinction matters here too.
	PenaltiesOrder                   *int `json:"penalties_order"`
	DirectFreekicksOrder             *int `json:"direct_freekicks_order"`
	CornersAndIndirectFreekicksOrder *int `json:"corners_and_indirect_freekicks_order"`

	// ScoutRisks flags things FPL's own scout has noticed — most commonly a
	// blank gameweek, occasionally an injury note. Empty for the overwhelming
	// majority of players; every real payload captured so far had it empty
	// for everyone, so the field shape here is inferred from the properties
	// and notes consumed by scout-risk handling rather than verified against
	// a populated example.
	ScoutRisks []ScoutRisk `json:"scout_risks"`
}

// ScoutRisk is one entry of a player's scout_risks list. See Player.ScoutRisks
// for how confidently this shape is known.
type ScoutRisk struct {
	Property string `json:"property"`
	Notes    string `json:"notes"`
	Gameweek *int   `json:"gameweek"`
}

// Team is one entry of bootstrap.teams.
type Team struct {
	ID        int    `json:"id"`
	Code      int    `json:"code"`
	Name      string `json:"name"`
	ShortName string `json:"short_name"`

	// Null until the league table exists, hence pointers.
	Strength *int `json:"strength"`
	Form     *Num `json:"form"`

	Position int `json:"position"`
	Played   int `json:"played"`
	Points   int `json:"points"`

	// Dynamic strength ratings, updated weekly by FPL. Finer-grained than the
	// static FDR the fixture multiplier currently keys off.
	StrengthOverallHome int `json:"strength_overall_home"`
	StrengthOverallAway int `json:"strength_overall_away"`
	StrengthAttackHome  int `json:"strength_attack_home"`
	StrengthAttackAway  int `json:"strength_attack_away"`
	StrengthDefenceHome int `json:"strength_defence_home"`
	StrengthDefenceAway int `json:"strength_defence_away"`
}

// Event is a gameweek.
type Event struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	DeadlineTime string `json:"deadline_time"`

	Finished    bool `json:"finished"`
	DataChecked bool `json:"data_checked"`
	IsPrevious  bool `json:"is_previous"`
	IsCurrent   bool `json:"is_current"`
	IsNext      bool `json:"is_next"`

	AverageEntryScore int  `json:"average_entry_score"`
	HighestScore      *int `json:"highest_score"`
	MostCaptained     *int `json:"most_captained"`
	TopElement        *int `json:"top_element"`

	ChipPlays []ChipPlay `json:"chip_plays"`
}

// ChipPlay is the per-gameweek count of each chip used across all managers.
type ChipPlay struct {
	ChipName  string `json:"chip_name"`
	NumPlayed int    `json:"num_played"`
}

// ElementType is a position (GKP/DEF/MID/FWD).
type ElementType struct {
	ID                int    `json:"id"`
	SingularNameShort string `json:"singular_name_short"`
	PluralNameShort   string `json:"plural_name_short"`
	SquadSelect       int    `json:"squad_select"`
}

// Fixture is one entry of GET /fixtures/.
type Fixture struct {
	ID   int `json:"id"`
	Code int `json:"code"`
	// Event is null for a fixture not yet assigned to a gameweek
	// (postponements, rescheduled ties).
	Event *int `json:"event"`

	TeamH int `json:"team_h"`
	TeamA int `json:"team_a"`
	// Scores are null until the match is played.
	TeamHScore *int `json:"team_h_score"`
	TeamAScore *int `json:"team_a_score"`

	TeamHDifficulty int `json:"team_h_difficulty"`
	TeamADifficulty int `json:"team_a_difficulty"`

	KickoffTime         string `json:"kickoff_time"`
	Started             bool   `json:"started"`
	Finished            bool   `json:"finished"`
	FinishedProvisional bool   `json:"finished_provisional"`
	Minutes             int    `json:"minutes"`

	Stats []any `json:"stats"`
}

// EventOf returns the fixture's gameweek and whether one is assigned.
func (f Fixture) EventOf() (int, bool) {
	if f.Event == nil {
		return 0, false
	}
	return *f.Event, true
}

// InGameweek reports whether the fixture belongs to gw. An unassigned fixture
// never belongs to a gameweek.
func (f Fixture) InGameweek(gw int) bool {
	e, ok := f.EventOf()
	return ok && e == gw
}

// CurrentGameweek returns the current gameweek, else the next, else the last
// finished, else 1.
func (b *Bootstrap) CurrentGameweek() int {
	for _, e := range b.Events {
		if e.IsCurrent {
			return e.ID
		}
	}
	for _, e := range b.Events {
		if e.IsNext {
			return e.ID
		}
	}
	last := 0
	for _, e := range b.Events {
		if e.Finished {
			last = e.ID
		}
	}
	if last != 0 {
		return last
	}
	return 1
}

// NextGameweek mirrors fpl_client.get_next_gameweek: the next gameweek, else
// whatever CurrentGameweek resolves to.
func (b *Bootstrap) NextGameweek() int {
	for _, e := range b.Events {
		if e.IsNext {
			return e.ID
		}
	}
	return b.CurrentGameweek()
}
