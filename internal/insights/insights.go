// Package insights selectively fetches and caches CSVs from
// olbauday/FPL-Core-Insights: per-match detail (Elo, minutes, shots, and
// more) that supplements the official FPL API for the current season.
//
// Unlike vaastav (frozen, historical, cached forever), this source refreshes
// twice daily from a live pipeline, so the cache carries a TTL instead of
// being permanent. And unlike vaastav, its identifiers are already FPL's
// own: player_id is the FPL element id, and team id/code match FPL's
// bootstrap teams exactly — verified directly against the live CSVs before
// writing this package (data/2025-2026/teams.csv: Arsenal is id=1, code=3,
// matching FPL's own bootstrap; data/2025-2026/players.csv: player_id=266 is
// Eze, again matching FPL's element id). So joining is a direct map lookup,
// not a fuzzy match — the "fallback" this package provides is simply
// reporting when an id isn't present, never guessing.
//
// The enrichment tier lags the base files: verified against the live repo,
// 2026-27's gameweek directories currently carry only fixtures.csv,
// matches.csv, player_gameweek_stats.csv, playermatchstats.csv, players.csv,
// playerstats.csv, and teams.csv — shots.csv, xg_by_minute.csv,
// average_positions.csv, momentum.csv, and the two *_enrichment.csv files
// only exist for 2025-26. Every caller must treat a missing file as
// "signal unavailable," not an error: see ErrNotAvailable.
package insights

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is FPL-Core-Insights' raw-content root, overridable on
// Client for tests.
const DefaultBaseURL = "https://raw.githubusercontent.com/olbauday/FPL-Core-Insights/main/data"

// DefaultTTL matches the source's twice-daily (07:30/17:30 UTC) refresh
// cadence: a cached file older than this is refetched.
const DefaultTTL = 12 * time.Hour

// ErrNotAvailable reports that a season/gameweek combination has no such
// file upstream (a 404) — expected for the current season's enrichment
// tier, or a gameweek that hasn't been played yet. Callers should degrade to
// FPL-API-only behavior and say so, not treat this as a fetch failure.
var ErrNotAvailable = errors.New("insights: not available")

// Client fetches and disk-caches FPL-Core-Insights CSVs.
type Client struct {
	CacheDir string
	BaseURL  string
	TTL      time.Duration
	HTTP     *http.Client

	// now is injected for deterministic TTL tests.
	now func() time.Time
}

// NewClient returns a Client caching fetched CSVs under cacheDir.
func NewClient(cacheDir string) *Client {
	return &Client{
		CacheDir: cacheDir, BaseURL: DefaultBaseURL, TTL: DefaultTTL,
		HTTP: &http.Client{Timeout: 30 * time.Second}, now: time.Now,
	}
}

// seasonFile fetches a season-level file (teams.csv, players.csv, ...) —
// not scoped to a gameweek.
func (c *Client) seasonFile(ctx context.Context, season, file string) ([]byte, error) {
	return c.fetch(ctx, season+"/"+file)
}

// gameweekFile fetches a per-gameweek file under "By Gameweek/GW{n}/".
func (c *Client) gameweekFile(ctx context.Context, season string, gw int, file string) ([]byte, error) {
	return c.fetch(ctx, fmt.Sprintf("%s/By Gameweek/GW%d/%s", season, gw, file))
}

// fetch returns relPath's bytes from the on-disk cache if present and
// within TTL, else from GitHub, caching the result. A 404 upstream returns
// ErrNotAvailable rather than a generic error, and is not cached — an
// absent file may appear later (a gameweek being played, the enrichment
// tier catching up), and a fixed 404 shouldn't require clearing the cache
// by hand.
func (c *Client) fetch(ctx context.Context, relPath string) ([]byte, error) {
	cachePath := filepath.Join(c.CacheDir, relPath)
	if info, err := os.Stat(cachePath); err == nil {
		if c.now().Sub(info.ModTime()) < c.TTL {
			if b, err := os.ReadFile(cachePath); err == nil {
				return b, nil
			}
		}
	}

	// The only special character any of these paths ever carries is the
	// literal space in "By Gameweek" — verified against the real repo
	// listing — so a plain replace is enough; no need for full path
	// segment-escaping.
	reqURL := c.BaseURL + "/" + strings.ReplaceAll(relPath, " ", "%20")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", relPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotAvailable
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %d", relPath, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", relPath, err)
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return nil, fmt.Errorf("cache %s: %w", relPath, err)
	}
	if err := os.WriteFile(cachePath, b, 0o644); err != nil {
		return nil, fmt.Errorf("cache %s: %w", relPath, err)
	}
	// Stamp the cache file with c.now() rather than leaving the OS's real
	// write-time mtime: the freshness check above also reads via c.now(), and
	// tests inject a fake clock that can be far from real wall-clock time.
	// Without this, freshness would silently compare a fake "now" against a
	// real mtime and never agree.
	if err := os.Chtimes(cachePath, c.now(), c.now()); err != nil {
		return nil, fmt.Errorf("cache %s: %w", relPath, err)
	}
	return b, nil
}

// decodeCSV parses b into rows keyed by header column name, the same
// generic shape internal/vaastav uses — FPL-Core-Insights carries dozens of
// wide, evolving schemas across its files, so a generic row avoids hand
// modelling columns no caller needs yet.
func decodeCSV(b []byte) ([]map[string]string, error) {
	r := csv.NewReader(strings.NewReader(string(b)))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse csv: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	header := records[0]
	rows := make([]map[string]string, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := make(map[string]string, len(header))
		for i, h := range header {
			if i < len(rec) {
				row[h] = rec[i]
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// SeasonFile fetches and decodes a season-level CSV (e.g. "teams.csv",
// "players.csv") into generic rows.
func (c *Client) SeasonFile(ctx context.Context, season, file string) ([]map[string]string, error) {
	b, err := c.seasonFile(ctx, season, file)
	if err != nil {
		return nil, err
	}
	return decodeCSV(b)
}

// GameweekFile fetches and decodes a per-gameweek CSV (e.g.
// "playermatchstats.csv", "shots.csv") into generic rows. Returns
// ErrNotAvailable if this file doesn't exist for the season/gameweek —
// expected for enrichment-tier files on a season that hasn't caught up yet,
// or a gameweek not yet played.
func (c *Client) GameweekFile(ctx context.Context, season string, gw int, file string) ([]map[string]string, error) {
	b, err := c.gameweekFile(ctx, season, gw, file)
	if err != nil {
		return nil, err
	}
	return decodeCSV(b)
}

// RowsByID indexes rows by the integer value of idField, for a direct-lookup
// join against FPL ids. Rows with a missing or unparseable id field are
// dropped rather than indexed under a bogus key.
func RowsByID(rows []map[string]string, idField string) map[int]map[string]string {
	out := make(map[int]map[string]string, len(rows))
	for _, row := range rows {
		id, ok := numInt(row[idField])
		if !ok {
			continue
		}
		out[id] = row
	}
	return out
}

// LookupID reports the row for id and whether it was found — the join
// "fallback" this package provides: a clear, typed miss, never a guess.
func LookupID(byID map[int]map[string]string, id int) (map[string]string, bool) {
	row, ok := byID[id]
	return row, ok
}

func numInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return int(f), true
}

// Float parses a CSV cell as a float, defaulting to 0 for blank or
// unparseable values — FPL-Core-Insights leaves genuinely inapplicable
// cells blank (e.g. shots.xgot on a blocked shot) rather than writing 0, so
// callers needing to distinguish "zero" from "not applicable" should check
// row[field] == "" themselves before calling this.
func Float(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

// Int parses a CSV cell as an int the same way Float does, truncating any
// decimal (FPL-Core-Insights writes some integer-valued columns as "4.0").
func Int(s string) int {
	return int(Float(s))
}

// TeamElo is teams.csv's FPL-id-keyed Elo rating — the single highest-value
// field this package exposes: FPL's own fixture difficulty is a static,
// hand-set 1-5 integer, while this is continuous and updated with results.
type TeamElo struct {
	TeamID int
	Elo    float64
}

// TeamElos fetches season's teams.csv and returns each team's Elo, keyed by
// FPL team id directly — no name mapping needed, see the package doc.
func (c *Client) TeamElos(ctx context.Context, season string) (map[int]TeamElo, error) {
	rows, err := c.SeasonFile(ctx, season, "teams.csv")
	if err != nil {
		return nil, err
	}
	out := make(map[int]TeamElo, len(rows))
	for _, row := range rows {
		id, ok := numInt(row["id"])
		if !ok {
			continue
		}
		out[id] = TeamElo{TeamID: id, Elo: Float(row["elo"])}
	}
	return out, nil
}
