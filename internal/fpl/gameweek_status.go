package fpl

import (
	"fmt"
	"math"
	"time"
)

// Gameweek states reported by GameweekStatus.
const (
	GameweekUpcoming   = "upcoming"
	GameweekInProgress = "in_progress"
	GameweekFinished   = "finished"
)

// GameweekStatus is where the season stands at a given moment: the current
// and next gameweek, each one's state, and the next transfer deadline.
//
// The fpl://status resource and fpl_manager_hub both report it, from the one
// StatusAt function, so the two cannot disagree.
type GameweekStatus struct {
	CurrentGameweek   int    `json:"current_gameweek"`
	CurrentGWState    string `json:"current_gw_state"`
	CurrentGWFinished bool   `json:"current_gw_finished"`
	NextGameweek      int    `json:"next_gameweek"`
	NextGWState       string `json:"next_gw_state"`
	// NextDeadline is the next gameweek's deadline in UTC (RFC 3339), or
	// "unknown" when the bootstrap has none.
	NextDeadline string `json:"next_deadline"`
	// TimeToDeadline is how long is left before NextDeadline, such as
	// "2d 5h", "3h 20m" or "45m". It is "passed" once the deadline has gone
	// and "unknown" when there is no deadline.
	TimeToDeadline     string  `json:"time_to_deadline"`
	GameweeksFinished  int     `json:"gameweeks_finished"`
	GameweeksRemaining int     `json:"gameweeks_remaining"`
	SeasonProgressPct  float64 `json:"season_progress_pct"`
}

// StatusAt reports the season's status as of now. now is a parameter, not
// read from the clock, so tests stay deterministic.
func StatusAt(b *Bootstrap, now time.Time) GameweekStatus {
	currentGW, nextGW := b.CurrentGameweek(), b.NextGameweek()
	current, _ := b.event(currentGW)
	next, nextFound := b.event(nextGW)

	finished := 0
	for _, e := range b.Events {
		if e.Finished {
			finished++
		}
	}

	s := GameweekStatus{
		CurrentGameweek:    currentGW,
		CurrentGWState:     eventState(current, now),
		CurrentGWFinished:  current.Finished,
		NextGameweek:       nextGW,
		NextGWState:        eventState(next, now),
		NextDeadline:       "unknown",
		TimeToDeadline:     "unknown",
		GameweeksFinished:  finished,
		GameweeksRemaining: 38 - finished,
		SeasonProgressPct:  math.Round(float64(finished)/38*100*10) / 10,
	}
	if !nextFound || next.DeadlineTime == "" {
		return s
	}
	deadline, err := time.Parse(time.RFC3339, next.DeadlineTime)
	if err != nil {
		// Report what FPL sent rather than hide it, but don't compute a
		// countdown from a value that doesn't parse.
		s.NextDeadline = next.DeadlineTime
		return s
	}
	s.NextDeadline = deadline.UTC().Format(time.RFC3339)
	s.TimeToDeadline = FormatTimeRemaining(deadline.Sub(now))
	return s
}

func (b *Bootstrap) event(id int) (Event, bool) {
	for _, e := range b.Events {
		if e.ID == id {
			return e, true
		}
	}
	return Event{}, false
}

// eventState is finished once FPL marks it so, in progress once its deadline
// has passed, and upcoming before that.
func eventState(e Event, now time.Time) string {
	if e.Finished {
		return GameweekFinished
	}
	deadline, err := time.Parse(time.RFC3339, e.DeadlineTime)
	if err == nil && !now.Before(deadline) {
		return GameweekInProgress
	}
	return GameweekUpcoming
}

// FormatTimeRemaining renders d as days and hours ("2d 5h"), as hours and
// minutes under a day ("3h 20m"), or as minutes under an hour ("45m"),
// rounding down. Under a minute is "<1m", and zero or less is "passed".
func FormatTimeRemaining(d time.Duration) string {
	if d <= 0 {
		return "passed"
	}
	mins := int(d / time.Minute)
	days, hours, minutes := mins/(24*60), mins/60%24, mins%60
	switch {
	case mins == 0:
		return "<1m"
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
