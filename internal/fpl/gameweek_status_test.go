package fpl

import (
	"testing"
	"time"
)

func statusBootstrap() *Bootstrap {
	return &Bootstrap{Events: []Event{
		{ID: 6, DeadlineTime: "2026-09-26T10:00:00Z", Finished: true, IsPrevious: true},
		{ID: 7, DeadlineTime: "2026-10-03T10:00:00Z", IsCurrent: true},
		{ID: 8, DeadlineTime: "2026-10-17T10:00:00Z", IsNext: true},
	}}
}

func TestStatusAt(t *testing.T) {
	// GW7 kicked off, GW8's deadline is 2 days 5 hours 30 minutes away.
	now := time.Date(2026, 10, 15, 4, 30, 0, 0, time.UTC)
	got := StatusAt(statusBootstrap(), now)

	want := GameweekStatus{
		CurrentGameweek: 7, CurrentGWState: GameweekInProgress, CurrentGWFinished: false,
		NextGameweek: 8, NextGWState: GameweekUpcoming,
		NextDeadline: "2026-10-17T10:00:00Z", TimeToDeadline: "2d 5h",
		GameweeksFinished: 1, GameweeksRemaining: 37, SeasonProgressPct: 2.6,
	}
	if got != want {
		t.Errorf("StatusAt =\n%+v\nwant\n%+v", got, want)
	}
}

func TestStatusAtCurrentFinished(t *testing.T) {
	b := statusBootstrap()
	b.Events[1].Finished = true
	got := StatusAt(b, time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
	if got.CurrentGWState != GameweekFinished || !got.CurrentGWFinished {
		t.Errorf("current state = %q (finished %v), want finished", got.CurrentGWState, got.CurrentGWFinished)
	}
	if got.GameweeksFinished != 2 {
		t.Errorf("GameweeksFinished = %d, want 2", got.GameweeksFinished)
	}
}

func TestStatusAtDeadlinePassed(t *testing.T) {
	got := StatusAt(statusBootstrap(), time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC))
	if got.TimeToDeadline != "passed" {
		t.Errorf("TimeToDeadline = %q, want passed", got.TimeToDeadline)
	}
	if got.NextGWState != GameweekInProgress {
		t.Errorf("NextGWState = %q, want in_progress", got.NextGWState)
	}
}

func TestStatusAtNoDeadline(t *testing.T) {
	b := statusBootstrap()
	b.Events[2].DeadlineTime = ""
	got := StatusAt(b, time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
	if got.NextDeadline != "unknown" || got.TimeToDeadline != "unknown" {
		t.Errorf("deadline = %q / %q, want unknown / unknown", got.NextDeadline, got.TimeToDeadline)
	}
}

func TestStatusAtNormalisesDeadlineToUTC(t *testing.T) {
	b := statusBootstrap()
	b.Events[2].DeadlineTime = "2026-10-17T11:00:00+01:00"
	got := StatusAt(b, time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC))
	if got.NextDeadline != "2026-10-17T10:00:00Z" {
		t.Errorf("NextDeadline = %q, want 2026-10-17T10:00:00Z", got.NextDeadline)
	}
	if got.TimeToDeadline != "1h 0m" {
		t.Errorf("TimeToDeadline = %q, want 1h 0m", got.TimeToDeadline)
	}
}

func TestFormatTimeRemaining(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{53*time.Hour + 59*time.Minute, "2d 5h"},
		{24 * time.Hour, "1d 0h"},
		{3*time.Hour + 20*time.Minute + 59*time.Second, "3h 20m"},
		{45 * time.Minute, "45m"},
		{30 * time.Second, "<1m"},
		{0, "passed"},
		{-time.Hour, "passed"},
	} {
		if got := FormatTimeRemaining(tc.d); got != tc.want {
			t.Errorf("FormatTimeRemaining(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
