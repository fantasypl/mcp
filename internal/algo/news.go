package algo

import (
	"fmt"
	"strings"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

// Ported from app/algorithms/news.py.
//
// The FPL `news` field is free text like "Hamstring - Expected back 15 Mar",
// and `news_added` is an ISO timestamp. These supplement
// chance_of_playing_next_round, which is often stale or optimistic.

// NegativeNewsKeywords are substrings marking a player as a transfer risk.
// Order matters only for readability; matching is any-of.
var NegativeNewsKeywords = []string{
	"unknown return",
	"expected back",
	"suspended",
	"international duty",
	"illness",
	"knock",
	"hamstring",
	"ankle",
	"knee",
	"thigh",
	"groin",
	"calf",
	"muscle",
	"ligament",
	"fracture",
	"concussion",
	"surgery",
	"operation",
	"personal reasons",
	"not in squad",
	"self-isolating",
	"match fitness",
}

// NewsPenaltyScore ports news.news_penalty_score.
//
//	-3.0  "unknown return" — worst, no timeline at all
//	-2.0  any other negative keyword
//	 0.0  nothing concerning
func NewsPenaltyScore(p *fpl.Player) float64 {
	if p.News == "" {
		return 0.0
	}
	lower := strings.ToLower(p.News)
	if strings.Contains(lower, "unknown return") {
		return -3.0
	}
	for _, kw := range NegativeNewsKeywords {
		if strings.Contains(lower, kw) {
			return -2.0
		}
	}
	return 0.0
}

// HasNegativeNews ports news.has_negative_news.
func HasNegativeNews(p *fpl.Player) bool {
	if p.News == "" {
		return false
	}
	lower := strings.ToLower(p.News)
	for _, kw := range NegativeNewsKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// FormatNewsAge ports news.format_news_age, rendering a news timestamp as a
// relative age. Returns "" where the Python returns None.
//
// now is passed explicitly rather than read from the clock so that output is
// reproducible; see Engine.Now.
func FormatNewsAge(newsAdded *string, now time.Time) string {
	if newsAdded == nil || *newsAdded == "" {
		return ""
	}
	added, ok := parseFPLTime(*newsAdded)
	if !ok {
		return ""
	}

	delta := now.Sub(added)
	// Python's timedelta.days floors toward negative infinity and .seconds is
	// the non-negative remainder, so a future timestamp yields days == -1
	// rather than 0. Reproduce by flooring.
	days := int(deltaFloorDays(delta))
	hours := int((delta - time.Duration(days)*24*time.Hour) / time.Hour)

	switch {
	case days == 0:
		switch {
		case hours == 0:
			return "just now"
		case hours == 1:
			return "1 hour ago"
		default:
			return fmt.Sprintf("%d hours ago", hours)
		}
	case days == 1:
		return "1 day ago"
	case days < 30:
		return fmt.Sprintf("%d days ago", days)
	case days < 60:
		return "1 month ago"
	default:
		return fmt.Sprintf("%d months ago", days/30)
	}
}

func deltaFloorDays(d time.Duration) int64 {
	day := int64(24 * time.Hour)
	n := int64(d)
	if n >= 0 {
		return n / day
	}
	// Floor division for negatives, matching Python.
	q := n / day
	if n%day != 0 {
		q--
	}
	return q
}

// parseFPLTime accepts the ISO-8601 shapes FPL emits, with or without a zone.
func parseFPLTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			// A naive timestamp is treated as UTC, as the Python does.
			if t.Location() == time.UTC || !strings.ContainsAny(s, "Z+") {
				return t.UTC(), true
			}
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// PlayerNews is the structured form of a player's news, or nil when there is
// none. Mirrors news.get_player_news.
type PlayerNews struct {
	Text    string `json:"text"`
	Updated string `json:"updated,omitempty"`
}

// GetPlayerNews ports news.get_player_news.
func GetPlayerNews(p *fpl.Player, now time.Time) *PlayerNews {
	text := strings.TrimSpace(p.News)
	if text == "" {
		return nil
	}
	out := &PlayerNews{Text: text}
	if age := FormatNewsAge(p.NewsAdded, now); age != "" {
		out.Updated = age
	}
	return out
}

// FormatNewsForReasoning ports news.format_news_for_reasoning, producing
// "Hamstring - Expected back 15 Mar (2 days ago)" or "" for no news.
func FormatNewsForReasoning(p *fpl.Player, now time.Time) string {
	info := GetPlayerNews(p, now)
	if info == nil {
		return ""
	}
	if info.Updated != "" {
		return info.Text + " (" + info.Updated + ")"
	}
	return info.Text
}
