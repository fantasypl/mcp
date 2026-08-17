// Package clubelo fetches Elo ratings from clubelo.com — the historical
// counterpart to FPL-Core-Insights' teams.csv:elo, which only carries a
// current snapshot and so can't back-test an Elo-driven fixture multiplier
// across past seasons.
//
// The formerly-keyless CSV API at api.clubelo.com is unreachable from the
// sandbox this package was built in — both plain HTTP and HTTPS requests to
// it hang and time out, which looks like a network-egress restriction
// rather than the service being down. clubelo.com's own website (a
// different host behind Cloudflare) *is* reachable, and its per-club pages
// (e.g. https://clubelo.com/Arsenal) are public — no login needed, verified
// live. This package scrapes those pages instead of the old API:
//
//   - The current Elo, from the page's "Elo: <b>NNNN</b>" header line.
//   - A recent Elo history, from a Vega-Lite chart spec embedded in the page
//     as `var vegaJson = {...};` — a real JSON blob, not fragile table-cell
//     scraping. As of this writing that history spans roughly the last four
//     seasons (2022-08 onward for Arsenal, verified live), not the full
//     1946-to-today range the old API offered; the site's dated ranking
//     pages that go further back require a login this package doesn't have.
//
// Team identity is also verified rather than guessed: slugByShortName below
// was built by fetching https://clubelo.com/ENG live and reading the real
// href for every Premier League club listed on it, not by pattern-guessing
// names.
package clubelo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
)

// DefaultBaseURL is clubelo.com's website root, overridable on Client for
// tests.
const DefaultBaseURL = "https://clubelo.com"

// DefaultTTL: ClubElo updates ratings once daily after each day's matches.
const DefaultTTL = 24 * time.Hour

// Rating is one point of a club's Elo history.
type Rating struct {
	// Date is "YYYY-MM-DD".
	Date string
	Elo  float64
}

// Client fetches and disk-caches clubelo.com club pages.
type Client struct {
	CacheDir string
	BaseURL  string
	TTL      time.Duration
	HTTP     *http.Client

	now func() time.Time
}

// NewClient returns a Client caching fetched pages under cacheDir.
func NewClient(cacheDir string) *Client {
	return &Client{
		CacheDir: cacheDir, BaseURL: DefaultBaseURL, TTL: DefaultTTL,
		HTTP: &http.Client{Timeout: 30 * time.Second}, now: time.Now,
	}
}

func (c *Client) fetchPage(ctx context.Context, slug string) ([]byte, error) {
	cachePath := filepath.Join(c.CacheDir, slug+".html")
	if info, err := os.Stat(cachePath); err == nil {
		if c.now().Sub(info.ModTime()) < c.TTL {
			if b, err := os.ReadFile(cachePath); err == nil {
				return b, nil
			}
		}
	}

	reqURL := c.BaseURL + "/" + slug
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", slug, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %d", slug, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", slug, err)
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return nil, fmt.Errorf("cache %s: %w", slug, err)
	}
	if err := os.WriteFile(cachePath, b, 0o644); err != nil {
		return nil, fmt.Errorf("cache %s: %w", slug, err)
	}
	// See internal/insights for why the cache file is stamped with c.now()
	// rather than left at its real write-time mtime.
	if err := os.Chtimes(cachePath, c.now(), c.now()); err != nil {
		return nil, fmt.Errorf("cache %s: %w", slug, err)
	}
	return b, nil
}

var currentEloRe = regexp.MustCompile(`Elo:\s*<b>(-?\d+)</b>`)

// Current returns slug's current Elo, parsed from the page's header line
// (an integer, as the site itself displays it).
func (c *Client) Current(ctx context.Context, slug string) (float64, error) {
	b, err := c.fetchPage(ctx, slug)
	if err != nil {
		return 0, err
	}
	m := currentEloRe.FindSubmatch(b)
	if m == nil {
		return 0, fmt.Errorf("clubelo: could not find current Elo on %s's page — page layout may have changed", slug)
	}
	v, err := strconv.ParseFloat(string(m[1]), 64)
	if err != nil {
		return 0, fmt.Errorf("clubelo: parse current Elo for %s: %w", slug, err)
	}
	return v, nil
}

// History returns slug's recent Elo history, oldest first — see the package
// doc for the window this actually covers.
func (c *Client) History(ctx context.Context, slug string) ([]Rating, error) {
	b, err := c.fetchPage(ctx, slug)
	if err != nil {
		return nil, err
	}
	points, err := extractVegaHistory(b)
	if err != nil {
		return nil, fmt.Errorf("clubelo: %s: %w", slug, err)
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Date < points[j].Date })
	return points, nil
}

// ByDate returns slug's Elo as of date ("YYYY-MM-DD") — the latest history
// point at or before date — and false if date predates the available
// history window (see the package doc), in which case no value is
// fabricated by extrapolating.
func (c *Client) ByDate(ctx context.Context, slug, date string) (Rating, bool, error) {
	hist, err := c.History(ctx, slug)
	if err != nil {
		return Rating{}, false, err
	}
	var best Rating
	found := false
	for _, r := range hist {
		if r.Date > date {
			break
		}
		best, found = r, true
	}
	return best, found, nil
}

// vegaSpec is the minimal shape needed out of the embedded Vega-Lite chart
// spec: which dataset holds the plotted points, and each point's Date/Elo.
type vegaSpec struct {
	Data struct {
		Name string `json:"name"`
	} `json:"data"`
	Datasets map[string][]struct {
		Date string  `json:"Date"`
		Elo  float64 `json:"Elo"`
	} `json:"datasets"`
}

var vegaJSONMarker = []byte("var vegaJson = ")

// extractVegaHistory finds `var vegaJson = {...};` in the page and decodes
// its Date/Elo series. It scans for the balanced closing brace rather than
// using a regex up to the first "};", since the JSON payload is large
// enough that a naive non-greedy match risks stopping early.
func extractVegaHistory(html []byte) ([]Rating, error) {
	start := bytes.Index(html, vegaJSONMarker)
	if start < 0 {
		return nil, fmt.Errorf("no embedded Elo chart data found on page")
	}
	start += len(vegaJSONMarker)

	end, err := matchingBrace(html, start)
	if err != nil {
		return nil, err
	}

	var spec vegaSpec
	if err := json.Unmarshal(html[start:end], &spec); err != nil {
		return nil, fmt.Errorf("decode embedded Elo chart data: %w", err)
	}
	points := spec.Datasets[spec.Data.Name]
	out := make([]Rating, 0, len(points))
	for _, p := range points {
		date := p.Date
		if len(date) >= 10 {
			date = date[:10] // "2026-05-30T00:00:00" -> "2026-05-30"
		}
		out = append(out, Rating{Date: date, Elo: p.Elo})
	}
	return out, nil
}

// matchingBrace returns the index just past the '}' that closes the '{' at
// or after start, tracking string literals so a brace inside a JSON string
// value doesn't end the scan early.
func matchingBrace(b []byte, start int) (int, error) {
	depth := 0
	inString := false
	escaped := false
	begun := false
	for i := start; i < len(b); i++ {
		ch := b[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
			begun = true
		case '}':
			depth--
			if begun && depth == 0 {
				return i + 1, nil
			}
		}
	}
	return 0, fmt.Errorf("unbalanced braces in embedded Elo chart data")
}

// slugByShortName maps FPL's stable team short_name to clubelo.com's own
// URL slug for that club. Current 2026-27 clubs were verified live against
// https://clubelo.com/ENG — every one of those was read as a real href on
// that page. Clubs relegated out of the Premier League before 2026-27 (LEI,
// SOU, LUT, SHU, IPS — needed to back-test across the seasons vaastav and
// ClubElo's history window both cover) don't appear on that live-fixtures
// page, so those five were verified individually instead: each URL was
// fetched directly and confirmed to return a real club page with a
// parseable Elo header. None of this table is guessed.
// A club not yet in this table is a clean, named failure via SlugFor rather
// than a silent miss.
var slugByShortName = map[string]string{
	"ARS": "Arsenal",
	"AVL": "AstonVilla",
	"BOU": "Bournemouth",
	"BRE": "Brentford",
	"BHA": "Brighton",
	"BUR": "Burnley",
	"CHE": "Chelsea",
	"CRY": "CrystalPalace",
	"EVE": "Everton",
	"FUL": "Fulham",
	"IPS": "Ipswich",
	"LEE": "Leeds",
	"LEI": "Leicester",
	"LIV": "Liverpool",
	"LUT": "Luton",
	"MCI": "ManCity",
	"MUN": "ManUnited",
	"NEW": "Newcastle",
	"NFO": "Forest",
	"SHU": "SheffieldUnited",
	"SOU": "Southampton",
	"SUN": "Sunderland",
	"TOT": "Tottenham",
	"WHU": "WestHam",
	"WOL": "Wolves",
}

// SlugFor returns clubelo.com's URL slug for the FPL team identified by
// shortName, and false if shortName isn't in the table — never a guess.
func SlugFor(shortName string) (string, bool) {
	s, ok := slugByShortName[shortName]
	return s, ok
}

// CurrentByFPLTeam fetches the current Elo for every team in fplTeams,
// keyed by FPL team id. Returns whatever it found alongside an error naming
// any team with no slug in the table — a caller may still use the partial
// result if it wants to degrade gracefully.
func (c *Client) CurrentByFPLTeam(ctx context.Context, fplTeams []fpl.Team) (map[int]float64, error) {
	out := make(map[int]float64, len(fplTeams))
	var unmatched []string
	for _, team := range fplTeams {
		slug, ok := SlugFor(team.ShortName)
		if !ok {
			unmatched = append(unmatched, fmt.Sprintf("%s (%s)", team.Name, team.ShortName))
			continue
		}
		elo, err := c.Current(ctx, slug)
		if err != nil {
			return out, fmt.Errorf("clubelo: %s: %w", team.Name, err)
		}
		out[team.ID] = elo
	}
	if len(unmatched) > 0 {
		return out, fmt.Errorf("clubelo: no slug for %d FPL team(s): %s", len(unmatched), joinComma(unmatched))
	}
	return out, nil
}

func joinComma(ss []string) string {
	out := ss[0]
	for _, s := range ss[1:] {
		out += ", " + s
	}
	return out
}
