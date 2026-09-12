// Package openfootball fetches and parses openfootball/champions-league's
// plain-text match schedules — a free, public-domain, self-hostable
// alternative to FPL-Core-Insights' cross-competition fixtures.csv, for the
// Champions League slice of the congestion signal specifically.
//
// Verified live against the real 2025-26 season file that this repo only
// carries the Champions League main tournament cleanly: Europa League and
// Conference League are qualifiers-only there (elq.txt/confq.txt, no
// group-stage-through-final data), and no other openfootball repo has them
// either. So this package — and the congestion signal it feeds — covers
// Champions League only; Europa League, Conference League, and domestic
// cups remain on FPL-Core-Insights. See CHANGELOG.md.
package openfootball

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is openfootball/champions-league's raw-content root,
// overridable on Client for tests and for pointing at a self-hosted mirror.
const DefaultBaseURL = "https://raw.githubusercontent.com/openfootball/champions-league/master"

// DefaultTTL matches the source's own refresh cadence — its commit history
// shows a weekly "auto-update week N" pattern during an active season.
const DefaultTTL = 7 * 24 * time.Hour

// ErrNotAvailable reports that a season's file doesn't exist upstream yet —
// e.g. before that competition's draw has happened. As of this writing
// there is no 2026-27 file yet for exactly this reason. Routine and
// expected, not a fetch failure; callers should degrade gracefully.
var ErrNotAvailable = errors.New("openfootball: not available")

// Match is one fixture's kickoff — teams as openfootball spells them
// (verbatim club name, no country suffix), not yet resolved to FPL teams.
type Match struct {
	Home, Away string
	Kickoff    time.Time
}

// Client fetches and disk-caches openfootball's season files.
type Client struct {
	CacheDir string
	BaseURL  string
	HTTP     *http.Client
	TTL      time.Duration

	now func() time.Time
}

// NewClient returns a Client caching fetched files under cacheDir.
func NewClient(cacheDir string) *Client {
	return &Client{
		CacheDir: cacheDir, BaseURL: DefaultBaseURL, TTL: DefaultTTL,
		HTTP: &http.Client{Timeout: 30 * time.Second}, now: time.Now,
	}
}

// Matches fetches and parses season's Champions League schedule (season in
// "YYYY-YY" form, e.g. "2025-26" — openfootball's own folder naming).
func (c *Client) Matches(ctx context.Context, season string) ([]Match, error) {
	b, err := c.fetch(ctx, season)
	if err != nil {
		return nil, err
	}
	return parseMatches(b)
}

func (c *Client) fetch(ctx context.Context, season string) ([]byte, error) {
	relPath := season + "/cl.txt"
	cachePath := filepath.Join(c.CacheDir, relPath)
	if info, err := os.Stat(cachePath); err == nil {
		if c.now().Sub(info.ModTime()) < c.TTL {
			if b, err := os.ReadFile(cachePath); err == nil {
				return b, nil
			}
		}
	}

	url := c.BaseURL + "/" + relPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openfootball: fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotAvailable
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openfootball: fetch %s: status %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openfootball: read %s: %w", url, err)
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return nil, fmt.Errorf("openfootball: cache %s: %w", relPath, err)
	}
	if err := os.WriteFile(cachePath, b, 0o644); err != nil {
		return nil, fmt.Errorf("openfootball: cache %s: %w", relPath, err)
	}
	return b, nil
}

// dateLineRe matches a football.txt date line: "Tue Sep 16 2025" or "Wed
// Sep 17" (year omitted — carried forward from the last date line that had
// one; verified live that the source always prints the year on the first
// date of a season and again whenever it changes, so there's nothing to
// infer beyond "reuse the last explicit year").
var dateLineRe = regexp.MustCompile(`^[A-Za-z]{3} ([A-Za-z]{3} \d{1,2})(?: (\d{4}))?$`)

// matchLineRe matches a match line, with or without a leading kickoff time
// (later matches at the same time as a preceding one omit it). Team fields
// reliably end in "(XXX)", a 2-4 letter country/federation code — a clean
// boundary that lets this ignore everything after the away team (scores,
// "a.e.t.", "pen." — congestion only needs date/time and identity, never
// the result), so it matches an unplayed future fixture identically to a
// finished one.
var matchLineRe = regexp.MustCompile(`^(?:(\d{2}:\d{2})\s+)?(.+?\([A-Za-z]{2,4}\))\s+v\s+(.+?\([A-Za-z]{2,4}\))`)

// parseMatches extracts every match's teams and kickoff from a football.txt
// season file. Section headers ("▪ League, Matchday 1"), the header block,
// and blank lines are simply lines neither regex matches, so no explicit
// handling is needed for them.
func parseMatches(b []byte) ([]Match, error) {
	var matches []Match
	var year string
	var date string    // "Sep 16", carried until the next date line
	var kickoff string // "18:45", carried until the next explicit time

	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if m := dateLineRe.FindStringSubmatch(line); m != nil {
			date = m[1]
			if m[2] != "" {
				year = m[2]
			}
			kickoff = "" // a new date always starts a fresh time block
			continue
		}

		if m := matchLineRe.FindStringSubmatch(line); m != nil {
			if date == "" || year == "" {
				continue // a match line before any date line is malformed input; skip rather than fail the whole parse
			}
			if m[1] != "" {
				kickoff = m[1]
			}
			if kickoff == "" {
				continue // no time ever established for this block
			}
			kt, err := time.Parse("Jan 2 2006 15:04", date+" "+year+" "+kickoff)
			if err != nil {
				continue
			}
			matches = append(matches, Match{
				Home:    strings.TrimSpace(m[2]),
				Away:    strings.TrimSpace(m[3]),
				Kickoff: kt,
			})
		}
	}
	return matches, nil
}

// stripCountry removes a trailing " (XXX)" country/federation suffix from
// an openfootball club name, e.g. "Arsenal FC (ENG)" -> "Arsenal FC".
func stripCountry(name string) string {
	if i := strings.LastIndex(name, " ("); i >= 0 && strings.HasSuffix(name, ")") {
		return name[:i]
	}
	return name
}

// englishClubNames maps FPL team short-names to their exact openfootball
// club name — verified against the real 2025-26 cl.txt (see
// openfootball_test.go), never guessed, matching clubelo.SlugFor's own
// standard. Only covers clubs actually observed playing Champions League
// football in that data; a club that qualifies in a future season without
// a verified entry here is simply not matched (FPLTeamFor reports false),
// degrading gracefully rather than guessing at its openfootball spelling.
var englishClubNames = map[string]string{
	"ARS": "Arsenal FC",
	"TOT": "Tottenham Hotspur FC",
	"MCI": "Manchester City FC",
	"LIV": "Liverpool FC",
	"CHE": "Chelsea FC",
	"NEW": "Newcastle United FC",
}

// FPLTeamFor returns the FPL short-name for openfootball's clubName
// (country suffix included or not), and false if clubName isn't a verified
// English club in the table above.
func FPLTeamFor(clubName string) (string, bool) {
	clubName = stripCountry(clubName)
	for short, name := range englishClubNames {
		if name == clubName {
			return short, true
		}
	}
	return "", false
}
