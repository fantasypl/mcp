package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/openfootball"
)

// runCongestionCapture archives openfootball/champions-league's Champions
// League schedule, resolved against the current bootstrap's FPL teams, to
// root/congestion/champions-league.json — the file internal/remotecongestion
// reads. Degrades to writing nothing, not an error, when the upstream
// season file doesn't exist yet (openfootball publishes a season's folder
// once that competition's draw happens) or when no FPL club is currently
// in the Champions League at all.
func runCongestionCapture(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("congestion-capture", flag.ExitOnError)
	root := fs.String("root", ".", "directory to write congestion/champions-league.json under")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client := fpl.NewClient()
	bootstrap, err := client.Bootstrap(ctx)
	if err != nil {
		return fmt.Errorf("fetch bootstrap: %w", err)
	}
	season := currentChampionsLeagueSeason(time.Now())

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("resolve cache dir: %w", err)
	}
	of := openfootball.NewClient(filepath.Join(cacheDir, "fplctl", "openfootball"))

	matches, err := of.Matches(ctx, season)
	if errors.Is(err, openfootball.ErrNotAvailable) {
		fmt.Printf("congestion-capture: %s not published upstream yet, nothing to write\n", season)
		return nil
	}
	if err != nil {
		return fmt.Errorf("fetch %s: %w", season, err)
	}

	codeByShortName := make(map[string]int, len(bootstrap.Teams))
	for _, t := range bootstrap.Teams {
		codeByShortName[t.ShortName] = t.Code
	}

	calendar := make(map[int][]time.Time)
	for _, m := range matches {
		for _, name := range [2]string{m.Home, m.Away} {
			short, ok := openfootball.FPLTeamFor(name)
			if !ok {
				continue
			}
			if code, ok := codeByShortName[short]; ok {
				calendar[code] = append(calendar[code], m.Kickoff)
			}
		}
	}
	if len(calendar) == 0 {
		fmt.Printf("congestion-capture: no FPL clubs found in %s Champions League schedule, nothing to write\n", season)
		return nil
	}
	for code := range calendar {
		sort.Slice(calendar[code], func(i, j int) bool { return calendar[code][i].Before(calendar[code][j]) })
	}

	out := make(map[string][]time.Time, len(calendar))
	for code, dates := range calendar {
		out[strconv.Itoa(code)] = dates
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(*root, "congestion", "champions-league.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote %s (%d FPL club(s))\n", path, len(calendar))
	return nil
}

// currentChampionsLeagueSeason mirrors internal/algo's own August-cutoff
// season boundary, in openfootball's "YYYY-YY" folder-naming form rather
// than that package's unexported "YYYY-YYYY" one.
func currentChampionsLeagueSeason(now time.Time) string {
	y := now.Year()
	if now.Month() < time.August {
		y--
	}
	return fmt.Sprintf("%d-%02d", y, (y+1)%100)
}
