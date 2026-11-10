package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// MaxSavedLeagues caps FPL_MCP_LEAGUES. Every saved league adds standings,
// squad and history requests to the tools that read them.
const MaxSavedLeagues = 5

// MaxTeamID is the largest FPL team ID the server accepts.
const MaxTeamID = 20_000_000

// SavedLeague is one mini-league from FPL_MCP_LEAGUES: its ID and an
// optional label such as "Work".
type SavedLeague struct {
	ID    int
	Label string
}

// Saved is what the user stored about themselves: their own team and the
// mini-leagues they compete in.
type Saved struct {
	TeamID  int // 0 when not set
	Leagues []SavedLeague
}

// SavedFromEnv reads FPL_MCP_TEAM_ID and FPL_MCP_LEAGUES. A malformed value
// is reported as an error that names the setting, and the rest still loads,
// so one typo does not hide the other setting.
func SavedFromEnv() (Saved, []error) {
	var out Saved
	var errs []error
	if v := strings.TrimSpace(os.Getenv("FPL_MCP_TEAM_ID")); v != "" {
		id, err := strconv.Atoi(v)
		if err != nil || id < 1 || id > MaxTeamID {
			errs = append(errs, fmt.Errorf("FPL_MCP_TEAM_ID %q is not an FPL team ID. Use the number after /entry/ in your FPL points page URL", v))
		} else {
			out.TeamID = id
		}
	}
	leagues, err := ParseLeagues(os.Getenv("FPL_MCP_LEAGUES"))
	if err != nil {
		errs = append(errs, err)
	}
	out.Leagues = leagues
	return out, errs
}

// ParseLeagues reads a list of mini-leagues separated by commas, semicolons
// or new lines. Each entry is a league ID, optionally followed by a colon
// and a label: "314159, 271828:Work". A full standings URL also works, so
// users can paste what their browser shows. Duplicates are dropped and the
// order is kept, since tools that take one league use the first.
func ParseLeagues(s string) ([]SavedLeague, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' })
	var out []SavedLeague
	seen := map[int]bool{}
	var bad []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		idPart, label := f, ""
		// A URL holds colons of its own, so only split a label off text
		// that isn't one.
		if !strings.Contains(f, "://") {
			if i := strings.Index(f, ":"); i >= 0 {
				idPart, label = strings.TrimSpace(f[:i]), strings.TrimSpace(f[i+1:])
			}
		}
		id, ok := leagueIDOf(idPart)
		if !ok {
			bad = append(bad, f)
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, SavedLeague{ID: id, Label: label})
	}
	if len(bad) > 0 {
		return out, fmt.Errorf("FPL_MCP_LEAGUES has entries that are not mini-league IDs: %s. Use the number after /leagues/ in the league's standings URL", strings.Join(bad, ", "))
	}
	if len(out) > MaxSavedLeagues {
		return out[:MaxSavedLeagues], fmt.Errorf("FPL_MCP_LEAGUES lists %d leagues; only the first %d are used", len(out), MaxSavedLeagues)
	}
	return out, nil
}

// leagueIDOf accepts a bare ID or a URL containing /leagues/ID/.
func leagueIDOf(s string) (int, bool) {
	if i := strings.Index(s, "/leagues/"); i >= 0 {
		s = s[i+len("/leagues/"):]
		if j := strings.IndexAny(s, "/?#"); j >= 0 {
			s = s[:j]
		}
	}
	id, err := strconv.Atoi(s)
	if err != nil || id < 1 {
		return 0, false
	}
	return id, true
}
