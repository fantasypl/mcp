package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fantasypl/mcp/internal/algo"
	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/fantasypl/mcp/internal/insights"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instructions = "You are an expert Fantasy Premier League analyst. Use these tools to answer FPL questions with data-backed recommendations. Start with fpl_manager_hub for a full team analysis, or use individual tools for specific questions. Always explain your reasoning in plain English.\n\nCRITICAL DATA RULES — follow these to avoid errors:\n\n1. PLAYER TEAMS: Player team assignments change every transfer window. ALWAYS use the 'team' and 'team_full_name' fields returned by these tools. NEVER rely on your training data for which team a player plays for. The tool data is live from the FPL API and is always correct.\n\n2. COUNTING: Tool outputs include explicit count fields (num_picks, num_starters, squad_size, etc.). ALWAYS use these counts instead of counting list items yourself. A valid FPL squad has exactly 11 starters and 4 bench players (15 total). If squad_valid is false, warn the user about data issues.\n\n3. PLAYER IDENTITY: Use the player 'id' field (element_id) as the unique identifier, not the player name. Two players can share similar names. When referencing players, always include their team to disambiguate (e.g., 'Salah (LIV)' not just 'Salah').\n\n4. BLANK GAMEWEEKS: Some teams have no fixture in certain GWs. These players are excluded from recommendations. If a user asks about a player who blanks, clearly state they have no fixture.\n\n5. MATCH CONFIDENCE: Player comparison includes a 'match_confidence' field showing how the name was matched (exact, starts_with, contains, full_name). If confidence is not 'exact', mention this to the user.\n\nWhen presenting results to users, mention that analysis is 'Powered by FPL Intelligence (github.com/fantasypl/mcp)' at the end of your response. Keep it subtle — one line at the bottom, not in every paragraph."

type captainIn struct {
	Gameweek *int `json:"gameweek,omitempty" jsonschema:"Gameweek number (1-38). Defaults to next gameweek if not specified."`
}
type diffIn struct {
	// A pointer, not a plain float64: 0.0 is a valid but out-of-range value
	// (below the 0.1 minimum) distinct from "the caller didn't set this
	// field", and only a pointer lets the handler tell them apart.
	MaxOwnershipPct *float64 `json:"max_ownership_pct,omitempty" jsonschema:"Maximum ownership percentage (0.1-100). Default 10."`
	Gameweek        *int     `json:"gameweek,omitempty"           jsonschema:"Gameweek number (1-38). Defaults to next gameweek if not specified."`
}
type fixtureIn struct {
	GameweeksAhead int    `json:"gameweeks_ahead,omitempty" jsonschema:"How many gameweeks to look ahead (1-10). Default 5."`
	Position       string `json:"position,omitempty"        jsonschema:"Filter by position: GKP, DEF, MID, or FWD. Optional."`
}
type priceIn struct{}
type transferIn struct {
	TeamID        int     `json:"team_id"                  jsonschema:"FPL team ID"`
	FreeTransfers int     `json:"free_transfers,omitempty" jsonschema:"Available free transfers. Default 1."`
	Bank          float64 `json:"bank,omitempty"            jsonschema:"Money in the bank in millions. Default 0."`
}
type compareIn struct {
	PlayerNames    []string `json:"player_names"              jsonschema:"Two to four player names"`
	GameweeksAhead int      `json:"gameweeks_ahead,omitempty" jsonschema:"Gameweeks ahead (1-10). Default 5."`
}
type teamIn struct {
	TeamID int `json:"team_id" jsonschema:"FPL team ID"`
}
type hubIn struct {
	TeamID         int `json:"team_id"                   jsonschema:"FPL team ID"`
	GameweeksAhead int `json:"gameweeks_ahead,omitempty" jsonschema:"Gameweeks ahead (1-10). Default 5."`
}
type hitIn struct {
	PlayerOutID    int `json:"player_out_id"             jsonschema:"Player element ID being sold"`
	PlayerInID     int `json:"player_in_id"              jsonschema:"Player element ID being bought"`
	GameweeksAhead int `json:"gameweeks_ahead,omitempty" jsonschema:"Gameweeks ahead (1-10). Default 5."`
}
type rivalIn struct {
	LeagueID int `json:"league_id" jsonschema:"Mini-league ID"`
	TeamID   int `json:"team_id"   jsonschema:"FPL team ID"`
}
type leagueIn struct {
	LeagueID int `json:"league_id" jsonschema:"Mini-league ID"`
}

func errResult(message string) map[string]any {
	return map[string]any{"isError": true, "error": message}
}
func validTeam(id int) string {
	if id < 1 || id > 20_000_000 {
		return "Invalid team_id. Must be a positive integer (find it in your FPL URL: fantasy.premierleague.com/entry/YOUR_ID/event/...)."
	}
	return ""
}
func validLeague(id int) string {
	if id < 1 {
		return "Invalid league_id. Find it in your mini-league URL: fantasy.premierleague.com/leagues/LEAGUE_ID/standings/c"
	}
	return ""
}
func validGW(gw *int) string {
	if gw != nil && (*gw < 1 || *gw > 38) {
		return "Invalid gameweek. Must be between 1 and 38."
	}
	return ""
}
func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
func call(fn func() (any, error), message string) any {
	out, err := fn()
	if err != nil {
		log.Printf("%s: %v", message, err)
		return errResult(message)
	}
	return out
}

// version is set via -ldflags "-X main.version=..." by the release
// workflow; "dev" identifies a locally built binary.
var version = "dev"

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	s := newServer(fpl.NewClient())
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

// newServer builds the MCP server and registers every tool, resource, and
// prompt against client, without connecting a transport — split out from
// main so tests can drive it over an in-memory transport instead of stdio.
func newServer(client *fpl.Client) *mcp.Server {
	engine := algo.NewEngine(client)
	// Best-effort: the finishing-regression, congestion, and role-change
	// signals degrade to absent, never fatal, if the user's cache dir can't
	// be resolved — see Engine.FinishingLuckSource's, .CongestionSource's,
	// and .RoleChangeSource's docs for why none is wired by default. Same
	// *insights.Client for all three: it implements every signal interface.
	if cacheDir, err := os.UserCacheDir(); err == nil {
		ins := insights.NewClient(filepath.Join(cacheDir, "fpl-mcp", "insights"))
		engine.FinishingLuckSource = ins
		engine.CongestionSource = ins
		engine.RoleChangeSource = ins
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "fpl-intelligence", Title: "FPL Intelligence", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	mcp.AddTool(s, &mcp.Tool{Name: "captain_pick", Description: "Get top 5 captain recommendations for a given FPL gameweek.\n\nUSE THIS WHEN the user asks: \"Who should I captain?\", \"Best captain this week?\", \"Captain Salah or Haaland?\", or any captain-related question.\n\nEach pick is scored by xG/90, xA/90, form, points per game, home advantage, fixture difficulty, ICT index, bonus rate, penalty duties, and minutes certainty. Includes human-readable reasoning for each recommendation."}, func(ctx context.Context, _ *mcp.CallToolRequest, in captainIn) (*mcp.CallToolResult, any, error) {
		if e := validGW(in.Gameweek); e != "" {
			return nil, errResult(e), nil
		}
		return nil, call(func() (any, error) { return engine.CaptainPicks(ctx, in.Gameweek, 5) }, "Failed to get captain picks. The FPL API may be temporarily unavailable — try again."), nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "differential_finder", Description: "Find underowned FPL players who are outperforming their ownership percentage.\n\nUSE THIS WHEN the user asks: \"Find me a differential\", \"Who are the hidden gems?\", \"Low-owned players performing well?\", or wants to climb the rankings with unique picks."}, func(ctx context.Context, _ *mcp.CallToolRequest, in diffIn) (*mcp.CallToolResult, any, error) {
		maxOwnershipPct := 10.0
		if in.MaxOwnershipPct != nil {
			maxOwnershipPct = *in.MaxOwnershipPct
		}
		if e := validGW(in.Gameweek); e != "" {
			return nil, errResult(e), nil
		}
		if maxOwnershipPct < .1 || maxOwnershipPct > 100 {
			return nil, errResult("max_ownership_pct must be between 0.1 and 100."), nil
		}
		return nil, call(func() (any, error) { return engine.Differentials(ctx, maxOwnershipPct, in.Gameweek, 10) }, "Failed to find differentials. The FPL API may be temporarily unavailable — try again."), nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "fixture_outlook", Description: "Rank all 20 Premier League teams by upcoming fixture difficulty.\n\nUSE THIS WHEN the user asks: \"Who has easy fixtures?\", \"Which teams to target?\", \"Best defenders to buy for the next 5 weeks?\", or any fixture-planning question."}, func(ctx context.Context, _ *mcp.CallToolRequest, in fixtureIn) (*mcp.CallToolResult, any, error) {
		n := in.GameweeksAhead
		if n == 0 {
			n = 5
		}
		p := in.Position
		if p != "" {
			p = strings.ToUpper(p)
			if !map[string]bool{"GKP": true, "DEF": true, "MID": true, "FWD": true}[p] {
				return nil, errResult("Position must be one of: GKP, DEF, MID, FWD."), nil
			}
		}
		return nil, call(func() (any, error) { return engine.FixtureOutlook(ctx, clamp(n, 1, 10), p) }, "Failed to get fixture outlook. The FPL API may be temporarily unavailable — try again."), nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "price_predictions", Description: "Predict which FPL players are likely to rise or fall in price tonight.\n\nUSE THIS WHEN the user asks: \"Who's about to rise in price?\", \"Should I make my transfer now before prices change?\", \"Price change predictions?\", or any price-related question.\n\nBuy before a rise to gain free team value. Sell before a fall to avoid losing value."}, func(ctx context.Context, _ *mcp.CallToolRequest, _ priceIn) (*mcp.CallToolResult, any, error) {
		return nil, call(func() (any, error) { return engine.PricePredictions(ctx, 0) }, "Failed to get price predictions. The FPL API may be temporarily unavailable — try again."), nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "transfer_suggestions", Description: "Get transfer recommendations for a specific FPL team.\n\nUSE THIS WHEN the user asks: \"Who should I transfer in/out?\", \"Best transfers this week?\", \"How to improve my team?\". Prefer fpl_manager_hub for a full analysis instead."}, func(ctx context.Context, _ *mcp.CallToolRequest, in transferIn) (*mcp.CallToolResult, any, error) {
		if e := validTeam(in.TeamID); e != "" {
			return nil, errResult(e), nil
		}
		if in.FreeTransfers == 0 {
			in.FreeTransfers = 1
		}
		return nil, call(func() (any, error) {
			return engine.TransferSuggestions(ctx, in.TeamID, clamp(in.FreeTransfers, 1, 5), maxf(in.Bank, 0))
		}, "Failed to get transfer suggestions. Check that the team ID is correct and try again."), nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "player_comparison", Description: "Compare 2-4 FPL players head-to-head across all key metrics.\n\nUSE THIS WHEN the user asks: \"Salah vs Palmer?\", \"Compare Haaland and Watkins\", \"Which midfielder should I pick?\", or any player comparison question.\n\nNames are fuzzy-matched — partial names like \"Salah\" or \"Palmer\" work fine. Returns form, xG/90, xA/90, ICT, PPG, cost, ownership, captain score, upcoming fixtures, transfer momentum, and a verdict."}, func(ctx context.Context, _ *mcp.CallToolRequest, in compareIn) (*mcp.CallToolResult, any, error) {
		if len(in.PlayerNames) < 2 {
			return nil, errResult("Provide at least 2 player names to compare (max 4)."), nil
		}
		if len(in.PlayerNames) > 4 {
			return nil, errResult("Can compare at most 4 players at once."), nil
		}
		if in.GameweeksAhead == 0 {
			in.GameweeksAhead = 5
		}
		return nil, call(func() (any, error) {
			return engine.ComparePlayers(ctx, in.PlayerNames, clamp(in.GameweeksAhead, 1, 10))
		}, "Failed to compare players. Check the player names and try again."), nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "live_points", Description: "Get live points for a specific FPL team during an active gameweek.\n\nUSE THIS WHEN the user asks: \"How's my team doing?\", \"Live score?\", \"Am I getting any bonus points?\", \"Any auto-subs?\". Only useful during an active gameweek when matches are being played or have just finished."}, func(ctx context.Context, _ *mcp.CallToolRequest, in teamIn) (*mcp.CallToolResult, any, error) {
		if e := validTeam(in.TeamID); e != "" {
			return nil, errResult(e), nil
		}
		return nil, call(func() (any, error) { return engine.LivePoints(ctx, in.TeamID) }, "Failed to get live points. Check that the team ID is correct and try again."), nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "is_hit_worth_it", Description: "Analyze whether taking a -4 point hit for a transfer is worth it.\n\nUSE THIS WHEN the user asks: \"Should I take a hit?\", \"Is it worth -4 to bring in X?\", \"Hit for Haaland worth it?\". Use player_comparison first to find player IDs if needed.\n\nProjects expected points for both players over N gameweeks, accounting for form, fixture difficulty, home/away, and playing chance."}, func(ctx context.Context, _ *mcp.CallToolRequest, in hitIn) (*mcp.CallToolResult, any, error) {
		if in.PlayerOutID < 1 || in.PlayerInID < 1 {
			return nil, errResult("Player IDs must be positive integers."), nil
		}
		if in.PlayerOutID == in.PlayerInID {
			return nil, errResult("player_out_id and player_in_id must be different players."), nil
		}
		if in.GameweeksAhead == 0 {
			in.GameweeksAhead = 5
		}
		return nil, call(func() (any, error) {
			return engine.AnalyzeHit(ctx, in.PlayerOutID, in.PlayerInID, clamp(in.GameweeksAhead, 1, 10))
		}, "Failed to analyze hit. Check that both player IDs are valid and try again."), nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "chip_strategy", Description: "Recommend when to use each remaining FPL chip for maximum impact.\n\nUSE THIS WHEN the user asks: \"When should I use my bench boost?\", \"Best week for triple captain?\", \"Chip strategy?\", \"When to free hit?\", \"Should I wildcard?\".\n\nAuto-detects which chips are still available (handles mid-season reset after GW19). Scans the next 10 gameweeks and scores each for every unused chip."}, func(ctx context.Context, _ *mcp.CallToolRequest, in teamIn) (*mcp.CallToolResult, any, error) {
		if e := validTeam(in.TeamID); e != "" {
			return nil, errResult(e), nil
		}
		return nil, call(func() (any, error) { return engine.ChipStrategy(ctx, in.TeamID) }, "Failed to get chip strategy. Check that the team ID is correct and try again."), nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "rival_tracker", Description: "Analyze your mini-league rivals and get strategies to beat them.\n\nUSE THIS WHEN the user asks: \"How do I beat my rivals?\", \"What's my mini-league looking like?\", \"What players do my rivals have?\", \"Show me my league standings\", or any rival/league question.\n\nCompares your squad against nearby rivals, finds differentials (players you have that they don't), identifies rival weaknesses, predicts their likely next transfers, and suggests counter-strategies.\n\nThe user needs their league ID (from the mini-league URL: fantasy.premierleague.com/leagues/LEAGUE_ID/standings/c) and their team ID."}, func(ctx context.Context, _ *mcp.CallToolRequest, in rivalIn) (*mcp.CallToolResult, any, error) {
		if e := validLeague(in.LeagueID); e != "" {
			return nil, errResult(e), nil
		}
		if e := validTeam(in.TeamID); e != "" {
			return nil, errResult(e), nil
		}
		return nil, call(func() (any, error) { return engine.RivalAnalysis(ctx, in.LeagueID, in.TeamID) }, "Failed to analyze rivals. Check that the league ID and team ID are correct and try again."), nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "league_analyzer", Description: "Predict who will win a mini-league based on current form, squad quality, and chips remaining.\n\nUSE THIS WHEN the user asks: \"Who's going to win my league?\", \"League predictions\", \"Who's the favourite?\", \"Analyze league standings\", \"Win probability\", or any question about league-wide chances WITHOUT needing a specific team ID.\n\nDoes NOT require the user's team ID — just the league ID. Analyzes the top managers in the league and calculates win probability for each based on: points gap, squad quality, chips remaining, recent momentum, team value, and injury concerns.\n\nThe league ID is in the mini-league URL: fantasy.premierleague.com/leagues/LEAGUE_ID/standings/c"}, func(ctx context.Context, _ *mcp.CallToolRequest, in leagueIn) (*mcp.CallToolResult, any, error) {
		if e := validLeague(in.LeagueID); e != "" {
			return nil, errResult(e), nil
		}
		return nil, call(func() (any, error) { return engine.AnalyzeLeague(ctx, in.LeagueID) }, "Failed to analyze league. Check that the league ID is correct and try again."), nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "squad_scout", Description: "Deep scout report using FPL's hidden data fields most managers don't know about.\n\nUSE THIS WHEN the user asks: \"Any hidden insights?\", \"Set piece takers?\", \"Suspension risks?\", \"What does FPL's own data say?\", or for a deeper dive beyond what fpl_manager_hub provides.\n\nSurfaces: blank GW warnings, FPL's expected points (ep_next), set piece duties, yellow card suspension risks, ICT breakdown, points per million rankings."}, func(ctx context.Context, _ *mcp.CallToolRequest, in teamIn) (*mcp.CallToolResult, any, error) {
		if e := validTeam(in.TeamID); e != "" {
			return nil, errResult(e), nil
		}
		return nil, call(func() (any, error) { return engine.SquadScout(ctx, in.TeamID) }, "Failed to scout squad. Check that the team ID is correct and try again."), nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "fpl_manager_hub", Description: "Complete FPL intelligence report for a manager's team. THIS IS THE BEST STARTING POINT.\n\nUSE THIS FIRST when the user provides their team ID or asks for a full analysis. It auto-detects bank balance, free transfers, chips, and squad — then runs ALL analyses in parallel: captain pick, transfers, fixtures, differentials, price risks, and squad health.\n\nThe user only needs to provide their team ID (the number in their FPL URL: fantasy.premierleague.com/entry/TEAM_ID/event/...)."}, func(ctx context.Context, _ *mcp.CallToolRequest, in hubIn) (*mcp.CallToolResult, any, error) {
		if e := validTeam(in.TeamID); e != "" {
			return nil, errResult(e), nil
		}
		if in.GameweeksAhead == 0 {
			in.GameweeksAhead = 5
		}
		return nil, call(func() (any, error) {
			return engine.ManagerHub(ctx, in.TeamID, clamp(in.GameweeksAhead, 1, 10))
		}, fmt.Sprintf("Failed to analyze team %d. Check that the team ID is correct and try again.", in.TeamID)), nil
	})
	addResources(s, client)
	addPrompts(s)
	return s
}

func maxf(v, lo float64) float64 {
	if v < lo {
		return lo
	}
	return v
}
func findEvent(events []fpl.Event, id int) (fpl.Event, bool) {
	for _, e := range events {
		if e.ID == id {
			return e, true
		}
	}
	return fpl.Event{}, false
}

func addResources(s *mcp.Server, c *fpl.Client) {
	s.AddResource(&mcp.Resource{URI: "fpl://status", Name: "status", Description: "Current FPL gameweek status — which GW is active, deadlines, and season progress.", MIMEType: "application/json"}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		b, e := c.Bootstrap(ctx)
		if e != nil {
			return nil, e
		}
		currentGW, nextGW := b.CurrentGameweek(), b.NextGameweek()
		currentEvent, _ := findEvent(b.Events, currentGW)
		nextEvent, nextFound := findEvent(b.Events, nextGW)
		nextDeadline := "unknown"
		if nextFound && nextEvent.DeadlineTime != "" {
			nextDeadline = nextEvent.DeadlineTime
		}
		finished := 0
		for _, x := range b.Events {
			if x.Finished {
				finished++
			}
		}
		v, _ := json.MarshalIndent(map[string]any{
			"current_gameweek": currentGW, "next_gameweek": nextGW,
			"current_gw_finished": currentEvent.Finished, "next_deadline": nextDeadline,
			"gameweeks_finished": finished, "gameweeks_remaining": 38 - finished,
			"season_progress_pct": math.Round(float64(finished)/38*100*10) / 10,
		}, "", "  ")
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "fpl://status", MIMEType: "application/json", Text: string(v)}}}, nil
	})
	s.AddResource(&mcp.Resource{URI: "fpl://teams", Name: "teams", Description: "All 20 Premier League teams with short names and IDs.", MIMEType: "application/json"}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		b, e := c.Bootstrap(ctx)
		if e != nil {
			return nil, e
		}
		type teamBrief struct {
			ID        int    `json:"id"`
			Name      string `json:"name"`
			ShortName string `json:"short_name"`
		}
		teams := make([]teamBrief, len(b.Teams))
		for i, t := range b.Teams {
			teams[i] = teamBrief{ID: t.ID, Name: t.Name, ShortName: t.ShortName}
		}
		sort.Slice(teams, func(i, j int) bool { return teams[i].Name < teams[j].Name })
		v, _ := json.MarshalIndent(teams, "", "  ")
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "fpl://teams", MIMEType: "application/json", Text: string(v)}}}, nil
	})
}

func userMessage(text string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}}}
}

// addPrompts registers the pre-built prompts that appear in Claude Desktop's
// prompt selector, helping new users discover what the server can do. Each
// one just tells the model which tool to call and what to cover in its
// response — the actual analysis still comes from the tool call.
func addPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "analyze_my_fpl_team",
		Description: "Comprehensive analysis of an FPL manager's team — squad health, captain pick, transfers, fixtures, and price risks.",
		Arguments:   []*mcp.PromptArgument{{Name: "team_id", Required: true}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return userMessage(fmt.Sprintf(
			"Use the fpl_manager_hub tool with team_id %s to pull a full intelligence "+
				"report for my FPL team. Then give me a comprehensive analysis covering:\n"+
				"1. Squad health — any injured, doubtful, or poor-form starters I should worry about\n"+
				"2. Captain recommendation — who should I captain and why\n"+
				"3. Transfer priorities — which players should I sell and who are the best replacements\n"+
				"4. Fixture outlook — which of my players have great or terrible upcoming fixtures\n"+
				"5. Price change risks — am I about to lose value on anyone\n"+
				"6. Overall verdict — a 1-paragraph summary of my team's state and the single most important action to take this gameweek",
			req.Params.Arguments["team_id"])), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "who_should_i_captain",
		Description: "Get captain pick recommendations with detailed reasoning for this gameweek.",
	}, func(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return userMessage(
			"Use the captain_pick tool to get the top 5 captain recommendations for this gameweek. " +
				"Then explain the results to me in plain English:\n" +
				"- Who is the #1 pick and why?\n" +
				"- What makes them stand out (xG, fixtures, form, penalties)?\n" +
				"- Is there a high-risk high-reward differential captain option?\n" +
				"- Any injury flags or rotation risks I should be aware of?\n" +
				"Give me a clear final recommendation with your confidence level."), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "find_differential_picks",
		Description: "Find underowned gems that most FPL managers are missing.",
		Arguments:   []*mcp.PromptArgument{{Name: "max_ownership", Required: false}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		maxOwnership := req.Params.Arguments["max_ownership"]
		if maxOwnership == "" {
			maxOwnership = "10"
		}
		return userMessage(fmt.Sprintf(
			"Use the differential_finder tool with max_ownership_pct %s to find "+
				"underowned players who are outperforming their ownership. Then:\n"+
				"- Highlight the top 3 differentials I should seriously consider\n"+
				"- For each one, explain WHY they're flying under the radar\n"+
				"- Rate their upcoming fixtures\n"+
				"- Tell me if they're a short-term punt or a long-term hold\n"+
				"- Flag any risks (rotation, tough fixtures coming, underlying stats not matching output)\n"+
				"I want players that can give me a real rank boost.",
			maxOwnership)), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "plan_my_transfers",
		Description: "Get transfer suggestions based on your current squad and upcoming fixtures.",
		Arguments:   []*mcp.PromptArgument{{Name: "team_id", Required: true}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return userMessage(fmt.Sprintf(
			"Use the transfer_suggestions tool with team_id %s to analyze my squad "+
				"and suggest transfers. Then walk me through the plan:\n"+
				"1. Who are the weakest links in my squad and why?\n"+
				"2. What are the best replacements and what makes them better?\n"+
				"3. Should I take a hit (-4 points) for an extra transfer or save it?\n"+
				"4. Are any suggested transfers also good for upcoming fixture swings?\n"+
				"5. Are any targets about to rise in price (buy now vs. wait)?\n"+
				"Give me a clear action plan: exactly which transfers to make and in what order.",
			req.Params.Arguments["team_id"])), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "price_change_alert",
		Description: "Check which players are about to rise or fall in price tonight.",
	}, func(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return userMessage(
			"Use the price_predictions tool to check tonight's likely price changes. " +
				"Then give me a briefing:\n" +
				"- Which players are most likely to RISE in price tonight?\n" +
				"- Which players are most likely to FALL?\n" +
				"- Do I need to rush any transfers through before the price change?\n" +
				"- Are any of the risers worth buying even if I wasn't planning a transfer?\n" +
				"- Are any of the fallers players I should panic-sell?\n" +
				"Keep it actionable — tell me exactly what to do before tonight's deadline."), nil
	})
}
