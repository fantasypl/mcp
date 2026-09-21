// Package footballdata reads the free season CSVs published by
// football-data.co.uk: results, referee and bookmaker odds for every
// Premier League match since the 1990s.
//
// It exists for one job — giving `fplctl odds-backtest` a long, public,
// key-free history of closing-line odds to measure market-implied
// probabilities against outcomes. Only the columns that job needs are
// parsed.
package footballdata

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fantasypl/mcp/internal/marketodds"
)

// DefaultBaseURL is the site's season-CSV root. The path segment for a
// season is its two-digit start and end years: "2025-26" -> "2526".
const userAgent = "fpl-mcp (personal FPL tool; github.com/fantasypl/mcp)"

const DefaultBaseURL = "https://www.football-data.co.uk/mmz4281"

// Match is one played (or scheduled) league fixture.
type Match struct {
	Date       time.Time
	Home, Away string // canonicalised to FPL team names, see Canonical
	HomeGoals  int
	AwayGoals  int
	Played     bool
	Referee    string
	Prices     marketodds.Prices // market average, pre-match
}

// Client fetches and disk-caches season CSVs.
type Client struct {
	CacheDir string
	BaseURL  string
	HTTP     *http.Client
	// CurrentTTL bounds how long the in-progress season's file is reused;
	// finished seasons never change and are cached forever.
	CurrentTTL time.Duration
	// RetryBackoff is the unit of the linear backoff between retries.
	RetryBackoff time.Duration
	now          func() time.Time
}

// NewClient returns a Client caching under cacheDir.
func NewClient(cacheDir string) *Client {
	return &Client{
		CacheDir: cacheDir, BaseURL: DefaultBaseURL,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
		CurrentTTL: 12 * time.Hour, RetryBackoff: 2 * time.Second,
		now: time.Now,
	}
}

// seasonPath converts "2025-26" to "2526".
func seasonPath(season string) (string, error) {
	if len(season) != 7 || season[4] != '-' {
		return "", fmt.Errorf("footballdata: season %q must look like 2025-26", season)
	}
	return season[2:4] + season[5:7], nil
}

// Season returns every match in season, oldest first.
func (c *Client) Season(ctx context.Context, season string) ([]Match, error) {
	sp, err := seasonPath(season)
	if err != nil {
		return nil, err
	}
	raw, err := c.fetch(ctx, sp)
	if err != nil {
		return nil, err
	}
	return parse(raw)
}

func (c *Client) fetch(ctx context.Context, sp string) ([]byte, error) {
	cachePath := filepath.Join(c.CacheDir, sp+"-E0.csv")
	if st, err := os.Stat(cachePath); err == nil {
		// A file is final only if it was downloaded after its season ended;
		// one cached mid-season must still age out.
		final := seasonEnded(sp, st.ModTime())
		if final || c.now().Sub(st.ModTime()) < c.CurrentTTL {
			if b, err := os.ReadFile(cachePath); err == nil {
				return b, nil
			}
		}
	}
	b, err := c.download(ctx, c.BaseURL+"/"+sp+"/E0.csv")
	if err != nil {
		return nil, fmt.Errorf("footballdata: fetch %s: %w", sp, err)
	}
	if err := os.MkdirAll(c.CacheDir, 0o755); err == nil {
		_ = os.WriteFile(cachePath, b, 0o644)
	}
	return b, nil
}

// seasonEnded reports whether the season identified by sp ("2526") finished
// before now — a season runs August to May, so it is over from 1 July of its
// end year.
func seasonEnded(sp string, now time.Time) bool {
	end, err := strconv.Atoi(sp[2:])
	if err != nil {
		return false
	}
	return now.After(time.Date(2000+end, time.July, 1, 0, 0, 0, 0, time.UTC))
}

func parse(raw []byte) ([]Match, error) {
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(raw), "\ufeff")))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("footballdata: read header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}
	for _, need := range []string{"Date", "HomeTeam", "AwayTeam"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("footballdata: missing column %q", need)
		}
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	num := func(rec []string, names ...string) float64 {
		for _, n := range names {
			if v, err := strconv.ParseFloat(get(rec, n), 64); err == nil && v > 0 {
				return v
			}
		}
		return 0
	}

	var out []Match
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("footballdata: read row: %w", err)
		}
		date, err := parseDate(get(rec, "Date"))
		if err != nil {
			continue // blank trailing rows
		}
		m := Match{
			Date: date, Home: Canonical(get(rec, "HomeTeam")), Away: Canonical(get(rec, "AwayTeam")),
			Referee: get(rec, "Referee"),
			// Market average first: it is present in every season and is
			// less noisy than any single bookmaker. Bet365 is the fallback.
			Prices: marketodds.Prices{
				Home:    num(rec, "AvgH", "B365H"),
				Draw:    num(rec, "AvgD", "B365D"),
				Away:    num(rec, "AvgA", "B365A"),
				Over25:  num(rec, "Avg>2.5", "B365>2.5"),
				Under25: num(rec, "Avg<2.5", "B365<2.5"),
			},
		}
		if hg, err1 := strconv.Atoi(get(rec, "FTHG")); err1 == nil {
			if ag, err2 := strconv.Atoi(get(rec, "FTAG")); err2 == nil {
				m.HomeGoals, m.AwayGoals, m.Played = hg, ag, true
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func parseDate(s string) (time.Time, error) {
	for _, layout := range []string{"02/01/2006", "02/01/06"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad date %q", s)
}

// aliases maps football-data.co.uk team names to the names FPL uses
// (bootstrap-static and vaastav's teams.csv). Names already identical, such
// as "Arsenal" or "Nott'm Forest", need no entry.
var aliases = map[string]string{
	"Man United":       "Man Utd",
	"Tottenham":        "Spurs",
	"Sheffield United": "Sheffield Utd",
}

// Canonical returns the FPL spelling of a football-data.co.uk team name.
func Canonical(name string) string {
	if a, ok := aliases[name]; ok {
		return a
	}
	return name
}

// download GETs url, retrying transient failures (5xx, 429, transport
// errors) a few times with growing backoff — the site occasionally returns
// a 503 under load.
func (c *Client) download(ctx context.Context, url string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * c.RetryBackoff):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		// The site answers Go's default User-Agent with a 503 (verified
		// live), but accepts any descriptive one.
		req.Header.Set("User-Agent", userAgent)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		b, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK && rerr == nil:
			return b, nil
		case resp.StatusCode == http.StatusOK:
			lastErr = rerr
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		default:
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
	}
	return nil, lastErr
}
