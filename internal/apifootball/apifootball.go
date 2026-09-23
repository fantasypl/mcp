// Package apifootball fetches upcoming Premier League match odds from
// API-Football (v3.football.api-sports.io) and converts them into
// per-match goal expectancy and clean-sheet probability via
// internal/marketodds.
//
// It is opt-in: it needs a personal API key in FPL_MCP_APIFOOTBALL_KEY, and
// without one nothing here runs. The free plan allows 100 requests a day, so
// results are cached on disk and one refresh costs a handful of requests
// (one fixtures call plus one odds page per ten matches).
//
// The response shapes follow API-Football's public v3 documentation. They
// are exercised in tests against recorded-shape fixtures, not a live key;
// the season-access limits of the free plan in particular are unverified.
// See Client.MatchModels for how failures surface.
package apifootball

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fantasypl/mcp/internal/marketodds"
)

const (
	// DefaultBaseURL is API-Football's direct endpoint.
	DefaultBaseURL = "https://v3.football.api-sports.io"
	// EnvKey names the environment variable holding the API key.
	EnvKey = "FPL_MCP_APIFOOTBALL_KEY"

	premierLeagueID = 39
	// DefaultTTL keeps a refresh to a few requests a day even under heavy
	// use; pre-match odds move slowly and API-Football itself only
	// snapshots them periodically.
	DefaultTTL = 6 * time.Hour

	betMatchWinner = 1 // API-Football bet id: values Home / Draw / Away
	betGoalsOU     = 5 // API-Football bet id: values "Over 2.5" / "Under 2.5"
)

// ErrNoKey means no API key is configured.
var ErrNoKey = errors.New("apifootball: no API key (set " + EnvKey + ")")

// Client talks to API-Football and caches the derived models on disk.
type Client struct {
	APIKey   string
	BaseURL  string
	CacheDir string
	TTL      time.Duration
	HTTP     *http.Client
	now      func() time.Time
}

// FromEnv returns a Client if the key env var is set, else nil.
func FromEnv(cacheDir string) *Client {
	key := strings.TrimSpace(os.Getenv(EnvKey))
	if key == "" {
		return nil
	}
	return &Client{
		APIKey: key, BaseURL: DefaultBaseURL, CacheDir: cacheDir, TTL: DefaultTTL,
		HTTP: &http.Client{Timeout: 20 * time.Second}, now: time.Now,
	}
}

// SeasonYear returns API-Football's season identifier for a date: the year
// the season starts in, and a season starts in August.
func SeasonYear(t time.Time) int {
	if t.Month() >= time.July {
		return t.Year()
	}
	return t.Year() - 1
}

type cacheFile struct {
	FetchedAt time.Time   `json:"fetched_at"`
	Season    int         `json:"season"`
	Matches   []cacheItem `json:"matches"`
}

type cacheItem struct {
	Key   marketodds.Key        `json:"key"`
	Model marketodds.MatchModel `json:"model"`
}

// MatchModels returns a model per upcoming Premier League match, keyed by
// FPL team names. It serves the disk cache while fresh; on a fetch failure
// it falls back to a stale cache rather than returning nothing, and only
// errors when there is neither.
func (c *Client) MatchModels(ctx context.Context) (map[marketodds.Key]marketodds.MatchModel, error) {
	if c == nil || c.APIKey == "" {
		return nil, ErrNoKey
	}
	season := SeasonYear(c.now())
	cached, haveCache := c.readCache(season)
	if haveCache && c.now().Sub(cached.FetchedAt) < c.TTL {
		return toMap(cached.Matches), nil
	}
	items, err := c.refresh(ctx, season)
	if err != nil {
		if haveCache {
			return toMap(cached.Matches), nil
		}
		return nil, err
	}
	c.writeCache(cacheFile{FetchedAt: c.now(), Season: season, Matches: items})
	return toMap(items), nil
}

func toMap(items []cacheItem) map[marketodds.Key]marketodds.MatchModel {
	m := make(map[marketodds.Key]marketodds.MatchModel, len(items))
	for _, it := range items {
		m[it.Key] = it.Model
	}
	return m
}

func (c *Client) cachePath(season int) string {
	return filepath.Join(c.CacheDir, fmt.Sprintf("odds-%d.json", season))
}

func (c *Client) readCache(season int) (cacheFile, bool) {
	b, err := os.ReadFile(c.cachePath(season))
	if err != nil {
		return cacheFile{}, false
	}
	var cf cacheFile
	if json.Unmarshal(b, &cf) != nil || cf.Season != season {
		return cacheFile{}, false
	}
	return cf, true
}

func (c *Client) writeCache(cf cacheFile) {
	if err := os.MkdirAll(c.CacheDir, 0o755); err != nil {
		return
	}
	if b, err := json.Marshal(cf); err == nil {
		_ = os.WriteFile(c.cachePath(cf.Season), b, 0o644)
	}
}

// envelope is API-Football's response wrapper. `errors` is an empty array
// on success but an object ({"plan": "..."}) on failure, so it stays raw.
type envelope struct {
	Errors   json.RawMessage              `json:"errors"`
	Paging   struct{ Current, Total int } `json:"paging"`
	Response json.RawMessage              `json:"response"`
}

func (c *Client) get(ctx context.Context, path string, q url.Values, into *envelope) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-apisports-key", c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("apifootball: %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("apifootball: %s: HTTP %d", path, resp.StatusCode)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("apifootball: %s: decode: %w", path, err)
	}
	if e := strings.TrimSpace(string(into.Errors)); e != "" && e != "[]" && e != "{}" && e != "null" {
		return fmt.Errorf("apifootball: %s: API error: %s", path, e)
	}
	return nil
}

type fixtureRow struct {
	Fixture struct{ ID int } `json:"fixture"`
	Teams   struct {
		Home struct{ Name string } `json:"home"`
		Away struct{ Name string } `json:"away"`
	} `json:"teams"`
}

type oddsRow struct {
	Fixture   struct{ ID int } `json:"fixture"`
	Bookmaker []struct {
		Bets []struct {
			ID     int `json:"id"`
			Values []struct {
				Value string `json:"value"`
				Odd   string `json:"odd"`
			} `json:"values"`
		} `json:"bets"`
	} `json:"bookmakers"`
}

// refresh does the network work: upcoming fixtures for the id->teams map,
// then odds pages, averaged across bookmakers.
func (c *Client) refresh(ctx context.Context, season int) ([]cacheItem, error) {
	base := url.Values{"league": {strconv.Itoa(premierLeagueID)}, "season": {strconv.Itoa(season)}}

	fq := url.Values{"league": base["league"], "season": base["season"], "status": {"NS"}}
	var fenv envelope
	if err := c.get(ctx, "/fixtures", fq, &fenv); err != nil {
		return nil, err
	}
	var fixtures []fixtureRow
	if err := json.Unmarshal(fenv.Response, &fixtures); err != nil {
		return nil, fmt.Errorf("apifootball: decode fixtures: %w", err)
	}
	teams := make(map[int]marketodds.Key, len(fixtures))
	for _, f := range fixtures {
		teams[f.Fixture.ID] = marketodds.Key{Home: Canonical(f.Teams.Home.Name), Away: Canonical(f.Teams.Away.Name)}
	}
	if len(teams) == 0 {
		return nil, fmt.Errorf("apifootball: no upcoming fixtures for season %d", season)
	}

	var out []cacheItem
	for page := 1; ; page++ {
		oq := url.Values{"league": base["league"], "season": base["season"], "page": {strconv.Itoa(page)}}
		var oenv envelope
		if err := c.get(ctx, "/odds", oq, &oenv); err != nil {
			return nil, err
		}
		var rows []oddsRow
		if err := json.Unmarshal(oenv.Response, &rows); err != nil {
			return nil, fmt.Errorf("apifootball: decode odds: %w", err)
		}
		for _, r := range rows {
			key, ok := teams[r.Fixture.ID]
			if !ok {
				continue
			}
			prices, ok := averagePrices(r)
			if !ok {
				continue
			}
			m, err := marketodds.Model(prices)
			if err != nil {
				continue
			}
			out = append(out, cacheItem{Key: key, Model: m})
		}
		if oenv.Paging.Total <= page || oenv.Paging.Total == 0 {
			break
		}
	}
	return out, nil
}

// averagePrices takes the mean decimal price across every bookmaker quoting
// a full 1X2 market, and (when quoted) the 2.5-goals line.
func averagePrices(r oddsRow) (marketodds.Prices, bool) {
	type acc struct{ sum, n float64 }
	var h, d, a, ov, un acc
	add := func(x *acc, s string) {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v > 1 {
			x.sum += v
			x.n++
		}
	}
	for _, bm := range r.Bookmaker {
		for _, bet := range bm.Bets {
			for _, v := range bet.Values {
				switch {
				case bet.ID == betMatchWinner && v.Value == "Home":
					add(&h, v.Odd)
				case bet.ID == betMatchWinner && v.Value == "Draw":
					add(&d, v.Odd)
				case bet.ID == betMatchWinner && v.Value == "Away":
					add(&a, v.Odd)
				case bet.ID == betGoalsOU && v.Value == "Over 2.5":
					add(&ov, v.Odd)
				case bet.ID == betGoalsOU && v.Value == "Under 2.5":
					add(&un, v.Odd)
				}
			}
		}
	}
	if h.n == 0 || d.n == 0 || a.n == 0 {
		return marketodds.Prices{}, false
	}
	p := marketodds.Prices{Home: h.sum / h.n, Draw: d.sum / d.n, Away: a.sum / a.n}
	if ov.n > 0 && un.n > 0 {
		p.Over25, p.Under25 = ov.sum/ov.n, un.sum/un.n
	}
	return p, true
}

// aliases maps API-Football team names to FPL's.
var aliases = map[string]string{
	"Manchester United": "Man Utd",
	"Manchester City":   "Man City",
	"Tottenham":         "Spurs",
	"Nottingham Forest": "Nott'm Forest",
	"Sheffield United":  "Sheffield Utd",
}

// Canonical returns FPL's spelling of an API-Football team name.
func Canonical(name string) string {
	if a, ok := aliases[name]; ok {
		return a
	}
	return name
}
