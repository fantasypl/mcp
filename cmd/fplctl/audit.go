package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/ajitem/fpl-intelligence/internal/algo"
	"github.com/ajitem/fpl-intelligence/internal/fpl"
)

// auditCheck ports accuracy_audit.py's AuditCheck dataclass — field names and
// JSON tags match exactly, since data/accuracy_audit.json is a compatibility
// boundary.
type auditCheck struct {
	Tool     string `json:"tool"`
	Check    string `json:"check"`
	Passed   bool   `json:"passed"`
	Severity string `json:"severity"` // "error" | "warning" | "info"
	Detail   string `json:"detail"`
}

const defaultAuditTeamID = 5293026

var auditCSVColumns = []string{"timestamp", "gameweek", "total_checks", "passed", "errors", "warnings", "pass_rate"}

func runAudit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	teamID := fs.Int("team-id", defaultAuditTeamID, "FPL team ID for team-dependent tools")
	toolFilter := fs.String("tool", "", "run checks for a single tool only")
	root := fs.String("root", ".", "project root; data/ lives under this")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client := fpl.NewClient()
	bootstrap, err := client.Bootstrap(ctx)
	if err != nil {
		return fmt.Errorf("fetch bootstrap: %w", err)
	}
	fixtures, err := client.Fixtures(ctx)
	if err != nil {
		return fmt.Errorf("fetch fixtures: %w", err)
	}
	engine := algo.NewEngine(client)
	nextGW := bootstrap.NextGameweek()

	playersByID := make(map[int]fpl.Player, len(bootstrap.Elements))
	for _, p := range bootstrap.Elements {
		playersByID[p.ID] = p
	}
	teamsByID := make(map[int]fpl.Team, len(bootstrap.Teams))
	for _, t := range bootstrap.Teams {
		teamsByID[t.ID] = t
	}
	blankingTeams := buildBlankingTeams(fixtures, nextGW)

	if len(blankingTeams) > 0 {
		names := make([]string, 0, len(blankingTeams))
		for tid := range blankingTeams {
			names = append(names, teamsByID[tid].ShortName)
		}
		sort.Strings(names)
		fmt.Printf("\n  Blank teams in GW%d: %v\n", nextGW, names)
	} else {
		fmt.Printf("\n  No blank teams in GW%d\n", nextGW)
	}

	type auditFn func() []auditCheck
	auditTasks := map[string]auditFn{
		"captain": func() []auditCheck { return auditCaptain(ctx, engine, playersByID, teamsByID, blankingTeams, nextGW) },
		"differentials": func() []auditCheck {
			return auditDifferentials(ctx, engine, playersByID, teamsByID, blankingTeams, nextGW)
		},
		"fixtures": func() []auditCheck {
			return auditFixtures(ctx, engine, bootstrap, playersByID, teamsByID, blankingTeams, nextGW)
		},
		"compare": func() []auditCheck {
			return auditCompare(ctx, engine, bootstrap, playersByID, teamsByID, blankingTeams, nextGW)
		},
		"prices":    func() []auditCheck { return auditPrices(ctx, engine, bootstrap, playersByID, teamsByID) },
		"transfers": func() []auditCheck { return auditTransfers(ctx, engine, *teamID) },
		"scout":     func() []auditCheck { return auditScout(ctx, engine, bootstrap, teamsByID, *teamID) },
		"chips":     func() []auditCheck { return auditChips(ctx, engine, bootstrap, *teamID) },
	}

	taskOrder := []string{"captain", "differentials", "fixtures", "compare", "prices", "transfers", "scout", "chips"}
	if *toolFilter != "" {
		if _, ok := auditTasks[*toolFilter]; !ok {
			return fmt.Errorf("unknown tool %q — available: captain, differentials, fixtures, compare, prices, transfers, scout, chips", *toolFilter)
		}
		taskOrder = []string{*toolFilter}
	}

	var allChecks []auditCheck
	for _, name := range taskOrder {
		allChecks = append(allChecks, auditTasks[name]()...)
	}
	allChecks = append(allChecks, checkStaleData(bootstrap)...)

	summary := printAuditReport(allChecks, nextGW)

	if err := saveAuditJSON(*root, allChecks, summary, nextGW); err != nil {
		return err
	}
	if err := appendAuditCSV(*root, summary, nextGW); err != nil {
		return err
	}

	if summary.Errors > 0 {
		return fmt.Errorf("%d audit check(s) failed", summary.Errors)
	}
	return nil
}

// buildBlankingTeams returns the set of team IDs with no fixture in gw, out
// of every team appearing anywhere in fixtures.
func buildBlankingTeams(fixtures []fpl.Fixture, gw int) map[int]bool {
	withFixture := map[int]bool{}
	all := map[int]bool{}
	for _, f := range fixtures {
		all[f.TeamH] = true
		all[f.TeamA] = true
		if f.InGameweek(gw) {
			withFixture[f.TeamH] = true
			withFixture[f.TeamA] = true
		}
	}
	blanking := map[int]bool{}
	for tid := range all {
		if !withFixture[tid] {
			blanking[tid] = true
		}
	}
	return blanking
}

func verifyTeam(playerID int, claimedTeam string, playersByID map[int]fpl.Player, teamsByID map[int]fpl.Team, tool string) *auditCheck {
	ref, ok := playersByID[playerID]
	if !ok {
		return &auditCheck{tool, "ghost_player", false, "error", fmt.Sprintf("Player ID %d not found in bootstrap data", playerID)}
	}
	actualTeam := teamsByID[ref.Team].ShortName
	if actualTeam == "" {
		actualTeam = "?"
	}
	if claimedTeam != actualTeam {
		return &auditCheck{tool, "team_assignment", false, "error",
			fmt.Sprintf("%s (ID %d): output says '%s', FPL API says '%s'", ref.WebName, playerID, claimedTeam, actualTeam)}
	}
	return nil
}

func verifyPosition(playerID int, claimedPos string, playersByID map[int]fpl.Player, tool string) *auditCheck {
	ref, ok := playersByID[playerID]
	if !ok {
		return nil // ghost player already caught
	}
	actualPos := algo.Position(ref.ElementType)
	if claimedPos != actualPos {
		return &auditCheck{tool, "position_mismatch", false, "error",
			fmt.Sprintf("%s: output says '%s', FPL API says '%s'", ref.WebName, claimedPos, actualPos)}
	}
	return nil
}

// --- generic JSON-shape helpers ---------------------------------------------
//
// Every audit function below inspects tool output the same way the Python
// does: as a generic dict, not a typed struct — accuracy_audit.py's whole
// point is validating the JSON contract an MCP client actually sees, which is
// exactly what round-tripping through encoding/json into map[string]any
// reproduces, regardless of which concrete Go type produced it.

func toMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func mList(m map[string]any, key string) []any {
	v, _ := m[key].([]any)
	return v
}

func mMap(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

func mStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func mInt(m map[string]any, key string) int {
	f, _ := m[key].(float64)
	return int(f)
}

func mFloat(m map[string]any, key string) float64 {
	f, _ := m[key].(float64)
	return f
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

var validPositions = map[string]bool{"GKP": true, "DEF": true, "MID": true, "FWD": true}

func auditCaptain(ctx context.Context, e *algo.Engine, playersByID map[int]fpl.Player, teamsByID map[int]fpl.Team, blankingTeams map[int]bool, nextGW int) []auditCheck {
	const tool = "captain"
	result, err := e.CaptainPicks(ctx, nil, 10)
	if err != nil {
		return []auditCheck{{tool, "tool_crash", false, "error", err.Error()}}
	}
	m := toMap(result)
	picks := mList(m, "picks")
	if len(picks) == 0 {
		return []auditCheck{{tool, "empty_output", false, "warning", "Captain picks returned 0 results"}}
	}

	var checks []auditCheck
	checks = append(checks, auditCheck{tool, "returns_results", true, "info", fmt.Sprintf("%d picks returned", len(picks))})

	seen := map[int]bool{}
	teamCounts := map[string]int{}
	for _, raw := range picks {
		pick := asMap(raw)
		player := mMap(pick, "player")
		pid := mInt(player, "id")
		team := mStr(player, "team")
		pos := mStr(player, "position")
		name := mStr(player, "name")

		ref, ok := playersByID[pid]
		if !ok {
			checks = append(checks, auditCheck{tool, "ghost_player", false, "error", fmt.Sprintf("Player ID %d (%s) not in FPL data", pid, name)})
			continue
		}
		if seen[pid] {
			checks = append(checks, auditCheck{tool, "duplicate_player", false, "error", fmt.Sprintf("%s appears multiple times", name)})
		}
		seen[pid] = true

		if c := verifyTeam(pid, team, playersByID, teamsByID, tool); c != nil {
			checks = append(checks, *c)
		}
		if c := verifyPosition(pid, pos, playersByID, tool); c != nil {
			checks = append(checks, *c)
		}
		if blankingTeams[ref.Team] {
			checks = append(checks, auditCheck{tool, "blank_gw_leak", false, "error",
				fmt.Sprintf("%s has no fixture in GW%d but appears in captain picks", name, nextGW)})
		}
		status := ref.Status
		if status == "" {
			status = "a"
		}
		if algo.InjuryStatuses[status] {
			cop := "None"
			if ref.ChanceOfPlayingNextRound != nil {
				cop = strconv.Itoa(*ref.ChanceOfPlayingNextRound)
			}
			checks = append(checks, auditCheck{tool, "injured_player_recommended", false, "warning",
				fmt.Sprintf("%s has status '%s' (chance=%s%%) but is recommended", name, status, cop)})
		}

		score, isNum := pick["score"].(float64)
		if !isNum || math.IsNaN(score) {
			checks = append(checks, auditCheck{tool, "invalid_score", false, "error", fmt.Sprintf("%s has invalid score: %v", name, pick["score"])})
		}

		teamCounts[team]++
	}

	for t, count := range teamCounts {
		if count >= 4 {
			checks = append(checks, auditCheck{tool, "team_diversity", false, "warning",
				fmt.Sprintf("%d of %d captain picks are from %s", count, len(picks), t)})
		}
	}

	if allPassed(checks) {
		checks = append(checks, auditCheck{tool, "all_checks", true, "info",
			fmt.Sprintf("All checks passed for %d picks (team/pos/status/blank/score validated)", len(picks))})
	}
	return checks
}

func auditDifferentials(ctx context.Context, e *algo.Engine, playersByID map[int]fpl.Player, teamsByID map[int]fpl.Team, blankingTeams map[int]bool, nextGW int) []auditCheck {
	const tool = "differentials"
	const maxOwn = 10.0
	result, err := e.Differentials(ctx, maxOwn, nil, 10)
	if err != nil {
		return []auditCheck{{tool, "tool_crash", false, "error", err.Error()}}
	}
	m := toMap(result)
	picks := mList(m, "differentials")
	if len(picks) == 0 {
		return []auditCheck{{tool, "empty_output", false, "warning", "Differentials returned 0 results"}}
	}

	var checks []auditCheck
	checks = append(checks, auditCheck{tool, "returns_results", true, "info", fmt.Sprintf("%d differentials returned", len(picks))})

	for _, raw := range picks {
		pick := asMap(raw)
		player := mMap(pick, "player")
		pid := mInt(player, "id")
		team := mStr(player, "team")
		pos := mStr(player, "position")
		name := mStr(player, "name")

		ref, ok := playersByID[pid]
		if !ok {
			checks = append(checks, auditCheck{tool, "ghost_player", false, "error", fmt.Sprintf("Player ID %d not in FPL data", pid)})
			continue
		}
		if c := verifyTeam(pid, team, playersByID, teamsByID, tool); c != nil {
			checks = append(checks, *c)
		}
		if c := verifyPosition(pid, pos, playersByID, tool); c != nil {
			checks = append(checks, *c)
		}

		actualOwn := ref.SelectedByPercent.Float()
		if actualOwn > maxOwn+0.5 {
			checks = append(checks, auditCheck{tool, "ownership_exceeded", false, "error",
				fmt.Sprintf("%s has %v%% ownership (threshold: %v%%)", name, actualOwn, maxOwn)})
		}
		if blankingTeams[ref.Team] {
			checks = append(checks, auditCheck{tool, "blank_gw_leak", false, "error", fmt.Sprintf("%s has no fixture in GW%d", name, nextGW)})
		}
		status := ref.Status
		if status == "" {
			status = "a"
		}
		if algo.InjuryStatuses[status] {
			checks = append(checks, auditCheck{tool, "injured_recommended", false, "error",
				fmt.Sprintf("%s has status '%s' but is recommended as differential", name, status)})
		}
	}

	if allPassed(checks) {
		checks = append(checks, auditCheck{tool, "all_checks", true, "info", "All differential checks passed"})
	}
	return checks
}

func auditFixtures(ctx context.Context, e *algo.Engine, bootstrap *fpl.Bootstrap, playersByID map[int]fpl.Player, teamsByID map[int]fpl.Team, blankingTeams map[int]bool, nextGW int) []auditCheck {
	const tool = "fixtures"
	result, err := e.FixtureOutlook(ctx, 5, "")
	if err != nil {
		return []auditCheck{{tool, "tool_crash", false, "error", err.Error()}}
	}
	m := toMap(result)
	teams := mList(m, "teams_by_difficulty")

	validTeamNames := map[string]bool{}
	for _, t := range bootstrap.Teams {
		validTeamNames[t.ShortName] = true
	}

	var checks []auditCheck
	if len(teams) != 20 {
		checks = append(checks, auditCheck{tool, "team_count", false, "error", fmt.Sprintf("Expected 20 teams, got %d", len(teams))})
	} else {
		checks = append(checks, auditCheck{tool, "team_count", true, "info", "All 20 teams present"})
	}
	for _, raw := range teams {
		t := asMap(raw)
		if name := mStr(t, "team"); !validTeamNames[name] {
			checks = append(checks, auditCheck{tool, "invalid_team_name", false, "error", fmt.Sprintf("Unknown team short name: %s", name)})
		}
	}

	for _, raw := range mList(m, "players_to_target") {
		p := asMap(raw)
		name := mStr(p, "name")
		team := mStr(p, "team")
		pos := mStr(p, "position")

		if !validTeamNames[team] {
			checks = append(checks, auditCheck{tool, "invalid_player_team", false, "error", fmt.Sprintf("%s has invalid team: %s", name, team)})
		}
		if !validPositions[pos] {
			checks = append(checks, auditCheck{tool, "invalid_position", false, "error", fmt.Sprintf("%s has invalid position: %s", name, pos)})
		}

		for _, pl := range bootstrap.Elements {
			if pl.WebName == name && teamsByID[pl.Team].ShortName == team {
				if blankingTeams[pl.Team] {
					checks = append(checks, auditCheck{tool, "blank_gw_leak", false, "error",
						fmt.Sprintf("%s (%s) has no fixture in GW%d but appears in players_to_target", name, team, nextGW)})
				}
				break
			}
		}
	}

	if allPassed(checks) {
		checks = append(checks, auditCheck{tool, "all_checks", true, "info", "All fixture checks passed"})
	}
	return checks
}

func auditCompare(ctx context.Context, e *algo.Engine, bootstrap *fpl.Bootstrap, playersByID map[int]fpl.Player, teamsByID map[int]fpl.Team, blankingTeams map[int]bool, nextGW int) []auditCheck {
	const tool = "compare"

	top := append([]fpl.Player(nil), bootstrap.Elements...)
	sort.SliceStable(top, func(i, j int) bool { return top[i].TotalPoints > top[j].TotalPoints })
	if len(top) > 2 {
		top = top[:2]
	}
	names := make([]string, 0, len(top))
	for _, p := range top {
		names = append(names, p.WebName)
	}

	result, err := e.ComparePlayers(ctx, names, 5)
	if err != nil {
		return []auditCheck{{tool, "tool_crash", false, "error", err.Error()}}
	}
	m := toMap(result)
	if errMsg := mStr(m, "error"); errMsg != "" {
		return []auditCheck{{tool, "match_error", false, "error", fmt.Sprintf("Compare failed: %s", errMsg)}}
	}

	profiles := mList(m, "players")
	if len(profiles) != 2 {
		return []auditCheck{{tool, "wrong_count", false, "error", fmt.Sprintf("Expected 2 profiles, got %d", len(profiles))}}
	}

	matchedNames := make([]string, 0, 2)
	for _, raw := range profiles {
		matchedNames = append(matchedNames, mStr(asMap(raw), "name"))
	}
	checks := []auditCheck{{tool, "players_matched", true, "info", fmt.Sprintf("Matched: %s, %s", matchedNames[0], matchedNames[1])}}

	for _, raw := range profiles {
		prof := asMap(raw)
		pid := mInt(prof, "id")
		ref, ok := playersByID[pid]
		if !ok {
			checks = append(checks, auditCheck{tool, "ghost_player", false, "error", fmt.Sprintf("Player ID %d not in FPL data", pid)})
			continue
		}

		if c := verifyTeam(pid, mStr(prof, "team"), playersByID, teamsByID, tool); c != nil {
			checks = append(checks, *c)
		}
		if c := verifyPosition(pid, mStr(prof, "position"), playersByID, tool); c != nil {
			checks = append(checks, *c)
		}

		expectedCost := float64(ref.NowCost) / 10
		if math.Abs(mFloat(prof, "cost")-expectedCost) > 0.1 {
			checks = append(checks, auditCheck{tool, "cost_mismatch", false, "error",
				fmt.Sprintf("%s: cost %v != FPL API %v", mStr(prof, "name"), prof["cost"], expectedCost)})
		}

		blankGWs := map[int]bool{}
		for _, g := range mList(prof, "blank_gameweeks") {
			if f, ok := g.(float64); ok {
				blankGWs[int(f)] = true
			}
		}
		if blankingTeams[ref.Team] && !blankGWs[nextGW] {
			checks = append(checks, auditCheck{tool, "blank_gw_not_flagged", false, "error",
				fmt.Sprintf("%s blanks GW%d but blank_gameweeks=%v", mStr(prof, "name"), nextGW, mList(prof, "blank_gameweeks"))})
		}
	}

	if allPassed(checks) {
		checks = append(checks, auditCheck{tool, "all_checks", true, "info", "All compare checks passed"})
	}
	return checks
}

func auditPrices(ctx context.Context, e *algo.Engine, bootstrap *fpl.Bootstrap, playersByID map[int]fpl.Player, teamsByID map[int]fpl.Team) []auditCheck {
	const tool = "prices"
	result, err := e.PricePredictions(ctx, 0)
	if err != nil {
		return []auditCheck{{tool, "tool_crash", false, "error", err.Error()}}
	}
	m := toMap(result)
	risers := mList(m, "likely_risers")
	fallers := mList(m, "likely_fallers")

	if len(risers) == 0 && len(fallers) == 0 {
		return []auditCheck{{tool, "empty_output", false, "info", "No price movers predicted (may be valid if no transfers yet)"}}
	}

	checks := []auditCheck{{tool, "returns_results", true, "info", fmt.Sprintf("%d risers, %d fallers", len(risers), len(fallers))}}

	validTeamNames := map[string]bool{}
	for _, t := range bootstrap.Teams {
		validTeamNames[t.ShortName] = true
	}

	all := append(append([]any{}, risers...), fallers...)
	for _, raw := range all {
		entry := asMap(raw)
		player := mMap(entry, "player")
		name := mStr(player, "name")
		team := mStr(player, "team")
		pos := mStr(player, "position")
		pid := mInt(player, "id")

		if !validTeamNames[team] {
			checks = append(checks, auditCheck{tool, "invalid_team", false, "error", fmt.Sprintf("%s has invalid team: %s", name, team)})
		}
		if !validPositions[pos] {
			checks = append(checks, auditCheck{tool, "invalid_position", false, "error", fmt.Sprintf("%s has invalid position: %s", name, pos)})
		}
		if pid != 0 {
			if _, ok := playersByID[pid]; ok {
				if c := verifyTeam(pid, team, playersByID, teamsByID, tool); c != nil {
					checks = append(checks, *c)
				}
			}
		}
	}

	if allPassed(checks) {
		checks = append(checks, auditCheck{tool, "all_checks", true, "info", "All price checks passed"})
	}
	return checks
}

// auditTransfers ports accuracy_audit.py's audit_transfers verbatim — including
// its bug. The Python reads result.get("suggestions", []), but
// get_transfer_suggestions actually returns the list under
// "transfer_suggestions" — so the Python's check always sees an empty list
// and short-circuits to the "no_suggestions" info check, never validating
// anything past that. Reproduced faithfully rather than fixed; see the port
// plan's Phase 7 for where such fixes belong.
func auditTransfers(ctx context.Context, e *algo.Engine, teamID int) []auditCheck {
	const tool = "transfers"
	result, err := e.TransferSuggestions(ctx, teamID, 1, 0.0)
	if err != nil {
		return []auditCheck{{tool, "tool_crash", false, "error", err.Error()}}
	}
	m := toMap(result)
	suggestions := mList(m, "suggestions")
	if len(suggestions) == 0 {
		return []auditCheck{{tool, "no_suggestions", true, "info", "No transfer suggestions (squad may be strong)"}}
	}
	return []auditCheck{{tool, "returns_results", true, "info", fmt.Sprintf("%d suggestions returned", len(suggestions))}}
}

func auditScout(ctx context.Context, e *algo.Engine, bootstrap *fpl.Bootstrap, teamsByID map[int]fpl.Team, teamID int) []auditCheck {
	const tool = "scout"
	result, err := e.SquadScout(ctx, teamID)
	if err != nil {
		return []auditCheck{{tool, "tool_crash", false, "error", err.Error()}}
	}
	m := toMap(result)
	squad := mList(m, "squad_report")
	if len(squad) == 0 {
		return []auditCheck{{tool, "empty_squad", false, "warning", "Scout returned empty squad (GW may not have started)"}}
	}

	checks := []auditCheck{{tool, "returns_results", true, "info", fmt.Sprintf("%d squad players returned", len(squad))}}

	validTeamNames := map[string]bool{}
	for _, t := range bootstrap.Teams {
		validTeamNames[t.ShortName] = true
	}

	for _, raw := range squad {
		p := asMap(raw)
		name := mStr(p, "name")
		team := mStr(p, "team")
		pos := mStr(p, "position")

		if !validTeamNames[team] {
			checks = append(checks, auditCheck{tool, "invalid_team", false, "error", fmt.Sprintf("%s has invalid team: %s", name, team)})
		}
		if !validPositions[pos] {
			checks = append(checks, auditCheck{tool, "invalid_position", false, "error", fmt.Sprintf("%s has invalid position: %s", name, pos)})
		}

		for _, pl := range bootstrap.Elements {
			if pl.WebName == name && teamsByID[pl.Team].ShortName == team {
				refEP := pl.EPNext.Float()
				toolEP := mFloat(p, "ep_next")
				if math.Abs(refEP-toolEP) > 0.5 {
					checks = append(checks, auditCheck{tool, "ep_next_mismatch", false, "warning",
						fmt.Sprintf("%s: ep_next %v vs FPL API %v", name, toolEP, refEP)})
				}
				break
			}
		}
	}

	if allPassed(checks) {
		checks = append(checks, auditCheck{tool, "all_checks", true, "info", "All scout checks passed"})
	}
	return checks
}

var validChips = map[string]bool{
	"wildcard": true, "bboost": true, "freehit": true, "3xc": true,
	"Wildcard": true, "Bench Boost": true, "Free Hit": true, "Triple Captain": true,
}

func auditChips(ctx context.Context, e *algo.Engine, bootstrap *fpl.Bootstrap, teamID int) []auditCheck {
	const tool = "chips"
	result, err := e.ChipStrategy(ctx, teamID)
	if err != nil {
		return []auditCheck{{tool, "tool_crash", false, "error", err.Error()}}
	}
	m := toMap(result)
	recs := mList(m, "recommendations")
	currentGW := bootstrap.CurrentGameweek()

	var checks []auditCheck
	for _, raw := range recs {
		rec := asMap(raw)
		chip := mStr(rec, "chip")
		bestGW, hasGW := rec["recommended_gameweek"].(float64)

		if !validChips[chip] {
			checks = append(checks, auditCheck{tool, "invalid_chip", false, "error", fmt.Sprintf("Unknown chip: %s", chip)})
		}
		if hasGW && int(bestGW) < currentGW {
			checks = append(checks, auditCheck{tool, "past_gw_recommended", false, "error",
				fmt.Sprintf("Chip %s recommended for GW%d which is in the past (current: GW%d)", chip, int(bestGW), currentGW)})
		}
		if hasGW && int(bestGW) > 38 {
			checks = append(checks, auditCheck{tool, "invalid_gw", false, "error",
				fmt.Sprintf("Chip %s recommended for GW%d (max is 38)", chip, int(bestGW))})
		}
	}
	checks = append(checks, auditCheck{tool, "returns_results", true, "info", fmt.Sprintf("%d chip recommendations", len(recs))})

	if allPassed(checks) {
		checks = append(checks, auditCheck{tool, "all_checks", true, "info", "All chip checks passed"})
	}
	return checks
}

func checkStaleData(bootstrap *fpl.Bootstrap) []auditCheck {
	var checks []auditCheck

	total := 0
	for _, p := range bootstrap.Elements {
		total += p.TransfersInEvent
	}
	if total == 0 {
		checks = append(checks, auditCheck{"data_quality", "stale_transfers", false, "warning", "All players have 0 transfers_in_event — API data may be stale"})
	} else {
		checks = append(checks, auditCheck{"data_quality", "transfers_active", true, "info", fmt.Sprintf("Total transfers_in_event: %d", total)})
	}

	if count := len(bootstrap.Elements); count < 500 {
		checks = append(checks, auditCheck{"data_quality", "low_player_count", false, "warning", fmt.Sprintf("Only %d players in bootstrap (expected ~700+)", count)})
	}
	if count := len(bootstrap.Teams); count != 20 {
		checks = append(checks, auditCheck{"data_quality", "wrong_team_count", false, "error", fmt.Sprintf("Expected 20 teams, got %d", count)})
	}
	return checks
}

func allPassed(checks []auditCheck) bool {
	for _, c := range checks {
		if !c.Passed {
			return false
		}
	}
	return true
}

type auditSummary struct {
	TotalChecks int     `json:"total_checks"`
	Passed      int     `json:"passed"`
	Errors      int     `json:"errors"`
	Warnings    int     `json:"warnings"`
	PassRatePct float64 `json:"pass_rate_pct"`
}

func printAuditReport(checks []auditCheck, gw int) auditSummary {
	var errs, warns, passed []auditCheck
	for _, c := range checks {
		switch {
		case c.Passed:
			passed = append(passed, c)
		case c.Severity == "error":
			errs = append(errs, c)
		case c.Severity == "warning":
			warns = append(warns, c)
		}
	}
	total := len(checks)
	passRate := 0.0
	if total > 0 {
		passRate = algo.Round(float64(len(passed))/float64(total)*100, 1)
	}

	fmt.Println()
	fmt.Println("============================================================")
	fmt.Printf("  FPL INTELLIGENCE — ACCURACY AUDIT (GW%d)\n", gw)
	fmt.Println("============================================================")
	fmt.Printf("\n  Total checks:  %d\n", total)
	fmt.Printf("  Passed:        %d\n", len(passed))
	fmt.Printf("  Errors:        %d\n", len(errs))
	fmt.Printf("  Warnings:      %d\n", len(warns))
	fmt.Printf("  Pass rate:     %v%%\n\n", passRate)

	if len(errs) > 0 {
		fmt.Println("  ERRORS")
		for _, c := range errs {
			fmt.Printf("  [%s] %s: %s\n", c.Tool, c.Check, c.Detail)
		}
		fmt.Println()
	}
	if len(warns) > 0 {
		fmt.Println("  WARNINGS")
		for _, c := range warns {
			fmt.Printf("  [%s] %s: %s\n", c.Tool, c.Check, c.Detail)
		}
		fmt.Println()
	}

	type toolStats struct{ checks, passed, errors, warnings int }
	stats := map[string]*toolStats{}
	var toolNames []string
	for _, c := range checks {
		s, ok := stats[c.Tool]
		if !ok {
			s = &toolStats{}
			stats[c.Tool] = s
			toolNames = append(toolNames, c.Tool)
		}
		s.checks++
		switch {
		case c.Passed:
			s.passed++
		case c.Severity == "error":
			s.errors++
		case c.Severity == "warning":
			s.warnings++
		}
	}
	sort.Strings(toolNames)
	fmt.Printf("  %-20s %7s %6s %5s %5s\n", "Tool", "Checks", "Pass", "Err", "Warn")
	for _, name := range toolNames {
		s := stats[name]
		status := "OK"
		if s.errors > 0 {
			status = "FAIL"
		}
		fmt.Printf("  %-20s %7d %6d %5d %5d  %s\n", name, s.checks, s.passed, s.errors, s.warnings, status)
	}
	fmt.Println("============================================================")

	return auditSummary{TotalChecks: total, Passed: len(passed), Errors: len(errs), Warnings: len(warns), PassRatePct: passRate}
}

func saveAuditJSON(root string, checks []auditCheck, summary auditSummary, gw int) error {
	path := filepath.Join(root, "data", "accuracy_audit.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var failures []auditCheck
	for _, c := range checks {
		if !c.Passed {
			failures = append(failures, c)
		}
	}
	report := struct {
		Timestamp string       `json:"timestamp"`
		Gameweek  int          `json:"gameweek"`
		Summary   auditSummary `json:"summary"`
		Checks    []auditCheck `json:"checks"`
		Failures  []auditCheck `json:"failures"`
	}{time.Now().UTC().Format(time.RFC3339), gw, summary, checks, failures}

	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("  Report saved to %s\n", path)
	return nil
}

func appendAuditCSV(root string, summary auditSummary, gw int) error {
	path := filepath.Join(root, "data", "accuracy_audit.csv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	writeHeader := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeHeader = true
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()
	if writeHeader {
		if err := w.Write(auditCSVColumns); err != nil {
			return err
		}
	}
	row := []string{
		time.Now().UTC().Format(time.RFC3339), strconv.Itoa(gw),
		strconv.Itoa(summary.TotalChecks), strconv.Itoa(summary.Passed),
		strconv.Itoa(summary.Errors), strconv.Itoa(summary.Warnings),
		algo.FloatStr(summary.PassRatePct),
	}
	if err := w.Write(row); err != nil {
		return err
	}
	fmt.Printf("  CSV appended to %s\n", path)
	return nil
}
