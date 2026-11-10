package algo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/fantasypl/mcp/internal/fpl"
)

// CompareOptions are player_comparison's optional extras. The zero value
// asks for none, so ComparePlayersWithOptions with it behaves exactly like
// ComparePlayersForTeam.
type CompareOptions struct {
	// TeamID adds buy legality per player; see ComparePlayersForTeam.
	TeamID *int
	// Fields names bootstrap-static element fields to pass through per
	// player, verbatim (issue #24).
	Fields []string
	// IncludeGameweeks adds the last N finished gameweeks per player and a
	// consistency measure over them (issue #25). 0 leaves them out.
	IncludeGameweeks int
}

// MaxIncludeGameweeks caps IncludeGameweeks at a full season.
const MaxIncludeGameweeks = 38

// Consistency thresholds, in FPL points for one gameweek (a double
// gameweek's matches summed). A haul is 10 or more, which is a goal and an
// assist with bonus for most players; a low score is 2 or fewer, which is
// an appearance and nothing else.
const (
	haulPoints     = 10
	lowScorePoints = 2
)

// GameweekHistoryNote explains the consistency measure in plain words. It
// rides along at the top level of any comparison that asked for
// include_gameweeks, so a reader need not infer the definitions.
const GameweekHistoryNote = "gameweek_history covers the same finished gameweeks for every player, so the numbers compare like for like. " +
	"A double gameweek's matches are summed into one row; a gameweek with no fixture is a row with blank=true and 0 points. " +
	"consistency is measured over those gameweeks: standard_deviation is how far a typical gameweek sits from the player's average (lower is steadier); " +
	"a haul is 10 or more points in a gameweek, and haul_share_pct is the share of the total that came from hauls; " +
	"a low score is 2 points or fewer, which includes not playing. " +
	"profile reads 'haul-dependent' when half or more of the points came from hauls, 'steady' when the standard deviation is at most half the average, " +
	"'low output' when the average is under 2 points, and 'mixed' otherwise. Fewer than 3 gameweeks is too few to judge."

// GameweekRow is one gameweek of a player's history.
type GameweekRow struct {
	Gameweek int `json:"gameweek"`
	// Opponents is e.g. ["ARS(H)"], two entries in a double gameweek and
	// none in a blank.
	Opponents []string `json:"opponents"`
	Blank     bool     `json:"blank"`
	Points    int      `json:"points"`
	Minutes   int      `json:"minutes"`
	Goals     int      `json:"goals"`
	Assists   int      `json:"assists"`
	XG        float64  `json:"xg"`
	XA        float64  `json:"xa"`
	Bonus     int      `json:"bonus"`
}

// Consistency summarises how a player's points were spread across the
// window. See GameweekHistoryNote for the definitions.
type Consistency struct {
	Gameweeks         int     `json:"gameweeks"`
	TotalPoints       int     `json:"total_points"`
	MeanPoints        float64 `json:"mean_points"`
	MedianPoints      float64 `json:"median_points"`
	StandardDeviation float64 `json:"standard_deviation"`
	Hauls             int     `json:"hauls"`
	HaulPoints        int     `json:"haul_points"`
	HaulSharePct      float64 `json:"haul_share_pct"`
	LowScores         int     `json:"low_scores"`
	Profile           string  `json:"profile"`
	Summary           string  `json:"summary"`
}

// GameweekHistory is one player's include_gameweeks block.
type GameweekHistory struct {
	FromGameweek int           `json:"from_gameweek"`
	ToGameweek   int           `json:"to_gameweek"`
	Gameweeks    []GameweekRow `json:"gameweeks"`
	Consistency  Consistency   `json:"consistency"`
}

// ComparePlayersWithOptions is ComparePlayersForTeam plus the extras in
// opts. Unknown field names are rejected before any comparison work, with
// the list of valid names, so a typo costs one cheap round trip.
func (e *Engine) ComparePlayersWithOptions(ctx context.Context, names []string, gameweeksAhead int, opts CompareOptions) (*CompareResult, error) {
	bootstrap, err := e.client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	fields := dedupe(opts.Fields)
	if len(fields) > 0 {
		if msg := validateElementFields(bootstrap, fields); msg != "" {
			return compareError(msg), nil
		}
	}
	if opts.IncludeGameweeks < 0 || opts.IncludeGameweeks > MaxIncludeGameweeks {
		return compareError(fmt.Sprintf("include_gameweeks must be between 1 and %d.", MaxIncludeGameweeks)), nil
	}

	res, err := e.ComparePlayersForTeam(ctx, names, gameweeksAhead, opts.TeamID)
	if err != nil || res.kind != compareKindSuccess {
		return res, err
	}

	byID := make(map[int]*fpl.Player, len(bootstrap.Elements))
	for i := range bootstrap.Elements {
		byID[bootstrap.Elements[i].ID] = &bootstrap.Elements[i]
	}

	if len(fields) > 0 {
		for i := range res.Players {
			prof := &res.Players[i]
			raw, err := byID[prof.ID].RawFields()
			if err != nil {
				return nil, err
			}
			prof.Fields = make(map[string]json.RawMessage, len(fields))
			for _, f := range fields {
				// Validated above against the union of every element's keys,
				// so a miss here is a field this one player lacks: null.
				v, ok := raw[f]
				if !ok {
					v = json.RawMessage("null")
				}
				prof.Fields[f] = v
			}
		}
	}

	if opts.IncludeGameweeks > 0 {
		from, to := gameweekWindow(bootstrap, opts.IncludeGameweeks)
		teams := teamsByID(bootstrap)
		for i := range res.Players {
			prof := &res.Players[i]
			// ComparePlayers fetched this summary moments ago, so the
			// client's cache answers it without a second request.
			summary, err := e.client.PlayerSummary(ctx, prof.ID)
			if err != nil {
				return nil, err
			}
			h := buildGameweekHistory(summary, from, to, teams)
			prof.GameweekHistory = &h
		}
		res.GameweekHistoryNote = GameweekHistoryNote
		if to == 0 {
			res.GameweekHistoryNote = "No gameweek has finished yet, so there is no history to show. " + GameweekHistoryNote
		}
	}
	return res, nil
}

// dedupe drops blank and repeated names, keeping first-seen order.
func dedupe(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// validateElementFields returns an error message naming every requested
// field no element carries, followed by the valid names, or "" when all
// are known. Valid means present on at least one element, so a field FPL
// adds mid-season is accepted without a code change.
func validateElementFields(b *fpl.Bootstrap, fields []string) string {
	valid := map[string]bool{}
	for i := range b.Elements {
		raw, err := b.Elements[i].RawFields()
		if err != nil || raw == nil {
			continue
		}
		for k := range raw {
			valid[k] = true
		}
	}
	var unknown []string
	for _, f := range fields {
		if !valid[f] {
			unknown = append(unknown, f)
		}
	}
	if len(unknown) == 0 {
		return ""
	}
	names := make([]string, 0, len(valid))
	for k := range valid {
		names = append(names, k)
	}
	sort.Strings(names)
	return fmt.Sprintf("Unknown field(s): %s. Valid fields: %s.", strings.Join(unknown, ", "), strings.Join(names, ", "))
}

// gameweekWindow returns the last n finished gameweeks as an inclusive
// range. A gameweek in progress is left out, since its points are not
// final. to is 0 when none has finished.
func gameweekWindow(b *fpl.Bootstrap, n int) (from, to int) {
	for _, ev := range b.Events {
		if ev.Finished && ev.ID > to {
			to = ev.ID
		}
	}
	if to == 0 {
		return 0, 0
	}
	return max(1, to-n+1), to
}

// buildGameweekHistory folds a player's match history into one row per
// gameweek in [from, to], blanks included, and measures consistency over
// those rows.
func buildGameweekHistory(summary *fpl.PlayerSummary, from, to int, teams map[int]*fpl.Team) GameweekHistory {
	h := GameweekHistory{FromGameweek: from, ToGameweek: to, Gameweeks: []GameweekRow{}}
	if to == 0 {
		h.Consistency = measureConsistency(nil)
		return h
	}

	rows := make([]GameweekRow, 0, to-from+1)
	index := make(map[int]int, to-from+1)
	for gw := from; gw <= to; gw++ {
		index[gw] = len(rows)
		rows = append(rows, GameweekRow{Gameweek: gw, Opponents: []string{}, Blank: true})
	}
	xg := make([]float64, len(rows))
	xa := make([]float64, len(rows))
	if summary != nil {
		for _, m := range summary.History {
			i, ok := index[m.Round]
			if !ok {
				continue
			}
			r := &rows[i]
			venue := "A"
			if m.WasHome {
				venue = "H"
			}
			r.Opponents = append(r.Opponents, fmt.Sprintf("%s(%s)", shortName(teams[m.OpponentTeam]), venue))
			r.Blank = false
			r.Points += m.TotalPoints
			r.Minutes += m.Minutes
			r.Goals += m.GoalsScored
			r.Assists += m.Assists
			r.Bonus += m.Bonus
			// Summed unrounded and rounded once, so a double gameweek's
			// xG is not off by a rounding step.
			xg[i] += m.ExpectedGoals.Float()
			xa[i] += m.ExpectedAssists.Float()
		}
	}
	points := make([]int, len(rows))
	for i := range rows {
		rows[i].XG = Round(xg[i], 2)
		rows[i].XA = Round(xa[i], 2)
		points[i] = rows[i].Points
	}
	h.Gameweeks = rows
	h.Consistency = measureConsistency(points)
	return h
}

// measureConsistency computes the Consistency block from per-gameweek
// points. See GameweekHistoryNote for what each number means.
func measureConsistency(points []int) Consistency {
	c := Consistency{Gameweeks: len(points)}
	if len(points) == 0 {
		c.Profile = "too few gameweeks"
		c.Summary = "No finished gameweeks to measure."
		return c
	}

	sum := 0
	for _, p := range points {
		sum += p
		if p >= haulPoints {
			c.Hauls++
			c.HaulPoints += p
		}
		if p <= lowScorePoints {
			c.LowScores++
		}
	}
	n := float64(len(points))
	mean := float64(sum) / n
	var sq float64
	for _, p := range points {
		d := float64(p) - mean
		sq += d * d
	}
	// Population standard deviation: these gameweeks are the whole record
	// being described, not a sample of a larger one.
	sd := math.Sqrt(sq / n)

	c.TotalPoints = sum
	c.MeanPoints = Round(mean, 1)
	c.MedianPoints = median(points)
	c.StandardDeviation = Round(sd, 1)
	if sum > 0 {
		c.HaulSharePct = Round(100*float64(c.HaulPoints)/float64(sum), 1)
	}

	switch {
	case len(points) < 3:
		c.Profile = "too few gameweeks"
	case mean < 2:
		c.Profile = "low output"
	case c.Hauls > 0 && 2*c.HaulPoints >= sum:
		c.Profile = "haul-dependent"
	case sd <= mean/2:
		c.Profile = "steady"
	default:
		c.Profile = "mixed"
	}
	c.Summary = consistencySummary(c)
	return c
}

// consistencySummary renders c as one or two plain sentences.
func consistencySummary(c Consistency) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d points over %d gameweek%s (%s a gameweek, typically %s either side).",
		c.TotalPoints, c.Gameweeks, plural(c.Gameweeks), FloatStr(c.MeanPoints), FloatStr(c.StandardDeviation))
	switch c.Hauls {
	case 0:
		b.WriteString(" No hauls of 10 or more.")
	case 1:
		fmt.Fprintf(&b, " One haul of 10 or more (%d points) made %s%% of the total.", c.HaulPoints, strconv.FormatFloat(c.HaulSharePct, 'f', -1, 64))
	default:
		fmt.Fprintf(&b, " %d hauls of 10 or more (%d points) made %s%% of the total.", c.Hauls, c.HaulPoints, strconv.FormatFloat(c.HaulSharePct, 'f', -1, 64))
	}
	if c.LowScores > 0 {
		fmt.Fprintf(&b, " %d gameweek%s of 2 points or fewer.", c.LowScores, plural(c.LowScores))
	}
	switch c.Profile {
	case "haul-dependent":
		b.WriteString(" Haul-dependent: most of the return came in a few big weeks.")
	case "steady":
		b.WriteString(" Steady: most weeks land close to the average.")
	case "mixed":
		b.WriteString(" Mixed: returns vary a lot from week to week without one haul carrying them.")
	case "low output":
		b.WriteString(" Low output: under 2 points a gameweek.")
	case "too few gameweeks":
		b.WriteString(" Too few gameweeks to judge consistency.")
	}
	return b.String()
}

func median(points []int) float64 {
	s := slices.Clone(points)
	slices.Sort(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return float64(s[mid])
	}
	return Round(float64(s[mid-1]+s[mid])/2, 1)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
