package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/fantasypl/mcp/internal/algo"
	"github.com/fantasypl/mcp/internal/config"
)

// defaults fills in team_id and league_id when a call leaves them out, from
// what the user saved: FPL_MCP_TEAM_ID and FPL_MCP_LEAGUES, then the FPL
// login's own team.
type defaults struct {
	saved config.Saved
	login algo.MyTeamSource // nil without the optional FPL login
	// problems are the saved settings that could not be read, shown in
	// place of "none saved" so a typo is not mistaken for a blank.
	problems []string
}

// loadDefaults reads the saved settings and gives the engine the leagues.
func loadDefaults(engine *algo.Engine) *defaults {
	saved, errs := config.SavedFromEnv()
	var problems []string
	for _, err := range errs {
		log.Printf("Saved settings: %v", err)
		problems = append(problems, err.Error()+".")
	}
	for _, l := range saved.Leagues {
		engine.Leagues = append(engine.Leagues, algo.LeagueRef{ID: l.ID, Label: l.Label})
	}
	return &defaults{saved: saved, login: engine.MyTeamSource, problems: problems}
}

const missingTeam = "No team_id given and none saved. Pass team_id (the number after /entry/ in your FPL points page URL), or save it in the extension's \"Your FPL team ID\" setting or as FPL_MCP_TEAM_ID."

// team returns id when given and valid, else the saved team, else the
// logged-in team. The string is an error message for the caller.
func (d *defaults) team(ctx context.Context, id int) (int, string) {
	if id != 0 {
		return id, validTeam(id)
	}
	if id = d.quietTeam(ctx); id != 0 {
		return id, ""
	}
	return 0, d.withProblems(missingTeam, "FPL_MCP_TEAM_ID")
}

// withProblems adds any saved-setting problem about key to msg.
func (d *defaults) withProblems(msg, key string) string {
	for _, p := range d.problems {
		if strings.HasPrefix(p, key) {
			msg += " The saved setting could not be used: " + p
		}
	}
	return msg
}

// quietTeam is the saved or logged-in team, or 0 when there is neither.
func (d *defaults) quietTeam(ctx context.Context) int {
	if d.saved.TeamID != 0 {
		return d.saved.TeamID
	}
	if d.login != nil {
		if id, err := d.login.MyEntry(ctx); err == nil {
			return id
		}
	}
	return 0
}

// league returns id when given and valid, else the first saved league.
func (d *defaults) league(id int) (int, string) {
	if id != 0 {
		return id, validLeague(id)
	}
	if len(d.saved.Leagues) > 0 {
		return d.saved.Leagues[0].ID, ""
	}
	return 0, d.withProblems("No league_id given and no leagues saved. Pass league_id (the number after /leagues/ in your mini-league's standings URL), or save your leagues in the extension's \"Your mini-leagues\" setting or as FPL_MCP_LEAGUES.", "FPL_MCP_LEAGUES")
}

// instructions tells the model what the user saved, so it does not ask for
// a team or league ID it already has.
func (d *defaults) instructions() string {
	if d.saved.TeamID == 0 && len(d.saved.Leagues) == 0 && d.login == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nSAVED SETTINGS: ")
	switch {
	case d.saved.TeamID != 0:
		fmt.Fprintf(&b, "The user's FPL team ID is %d. Tools use it when team_id is left out, so don't ask for it. ", d.saved.TeamID)
	case d.login != nil:
		b.WriteString("The user is logged in to FPL, so tools use their own team when team_id is left out. Don't ask for a team ID. ")
	}
	if len(d.saved.Leagues) > 0 {
		parts := make([]string, len(d.saved.Leagues))
		for i, l := range d.saved.Leagues {
			parts[i] = fmt.Sprint(l.ID)
			if l.Label != "" {
				parts[i] += " (" + l.Label + ")"
			}
		}
		fmt.Fprintf(&b, "They compete in these mini-leagues: %s. rival_tracker, league_analyzer and chip_strategy use the first when league_id is left out. "+
			"fpl_manager_hub reports every saved league, and captain_pick, transfer_suggestions and optimal_transfers add league_ownership: how the user's rivals hold each player. "+
			"Use it when advising: a shield or template player the user lacks is a risk to their league position, and a differential gains ground when he scores.",
			strings.Join(parts, ", "))
	}
	return b.String()
}
