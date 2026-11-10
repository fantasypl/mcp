package main

// Discovery: what a client shows a user before any tool runs. The list-result
// cache hint, the planning prompts and the two browsing resources added for
// issue #27 live here; the original five prompts and the status/teams
// resources stay in main.go.

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/fantasypl/mcp/internal/algo"
	"github.com/fantasypl/mcp/internal/fpl"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// listTTL is the cache hint sent with tools/list, prompts/list,
// resources/list and resources/templates/list.
//
// go-sdk v1.7.0 adds SEP-2549's "ttlMs" and "cacheScope" to every list
// result, and leaves ttlMs at 0, which the spec defines as "immediately
// stale". A client that honours that has nothing fresh to show when its
// prompt menu opens, which is the leading suspect for Claude Desktop's
// missing "Add from FPL Intelligence" entry (#27). These lists are fixed for
// the life of the process, so an hour is honest; a restart after an upgrade
// starts a new session, which a client lists afresh.
const listTTL = time.Hour

// listCacheMiddleware sets listTTL on the four list results. Every other
// result, resources/read included, keeps the SDK's default: resource
// contents are live FPL data and should not be cached by the client.
func listCacheMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if err != nil {
			return res, err
		}
		ms := int(listTTL / time.Millisecond)
		switch r := res.(type) {
		case *mcp.ListToolsResult:
			r.TTLMs = ms
		case *mcp.ListPromptsResult:
			r.TTLMs = ms
		case *mcp.ListResourcesResult:
			r.TTLMs = ms
		case *mcp.ListResourceTemplatesResult:
			r.TTLMs = ms
		}
		return res, nil
	}
}

// ---------------------------------------------------------------------------
// Resources
// ---------------------------------------------------------------------------

const (
	upcomingFixturesURI = "fpl://fixtures/upcoming"
	topPlayersURI       = "fpl://players/top"

	upcomingGameweeks = 5
	topPlayersCount   = 50
)

// upcomingFixture is one fixture in fpl://fixtures/upcoming. FDR is FPL's
// own 1-5 difficulty for each side, unblended, so it matches what the FPL
// site shows.
type upcomingFixture struct {
	Gameweek    int    `json:"gameweek"`
	KickoffTime string `json:"kickoff_time"`
	Home        string `json:"home"`
	Away        string `json:"away"`
	HomeFDR     int    `json:"home_fdr"`
	AwayFDR     int    `json:"away_fdr"`
}

type upcomingFixtures struct {
	FromGameweek int               `json:"from_gameweek"`
	ToGameweek   int               `json:"to_gameweek"`
	Note         string            `json:"note"`
	Fixtures     []upcomingFixture `json:"fixtures"`
}

// buildUpcomingFixtures lists every fixture in the next upcomingGameweeks
// gameweeks, by kickoff. Unscheduled fixtures (no gameweek) are left out.
func buildUpcomingFixtures(b *fpl.Bootstrap, fixtures []fpl.Fixture) upcomingFixtures {
	out := upcomingFixtures{
		Note:     "FDR is FPL's own fixture difficulty from 1 (easiest) to 5 (hardest), for the named side. Use fixture_outlook for a ranked, strength-adjusted view.",
		Fixtures: []upcomingFixture{},
	}
	next := b.NextGameweek()
	if next == 0 {
		out.Note = "The season has no gameweeks left. " + out.Note
		return out
	}
	out.FromGameweek, out.ToGameweek = next, min(next+upcomingGameweeks-1, 38)

	short := make(map[int]string, len(b.Teams))
	for _, t := range b.Teams {
		short[t.ID] = t.ShortName
	}
	for _, f := range fixtures {
		if f.Event == nil || *f.Event < out.FromGameweek || *f.Event > out.ToGameweek {
			continue
		}
		out.Fixtures = append(out.Fixtures, upcomingFixture{
			Gameweek: *f.Event, KickoffTime: f.KickoffTime,
			Home: short[f.TeamH], Away: short[f.TeamA],
			HomeFDR: f.TeamHDifficulty, AwayFDR: f.TeamADifficulty,
		})
	}
	slices.SortStableFunc(out.Fixtures, func(a, b upcomingFixture) int {
		return cmp.Or(cmp.Compare(a.Gameweek, b.Gameweek), cmp.Compare(a.KickoffTime, b.KickoffTime))
	})
	return out
}

// topPlayer is one row of fpl://players/top.
type topPlayer struct {
	ID            int     `json:"id"`
	Name          string  `json:"name"`
	Team          string  `json:"team"`
	Position      string  `json:"position"`
	Cost          float64 `json:"cost"`
	Form          float64 `json:"form"`
	TotalPoints   int     `json:"total_points"`
	PointsPerGame float64 `json:"points_per_game"`
	OwnershipPct  float64 `json:"ownership_pct"`
	Status        string  `json:"status"`
}

type topPlayers struct {
	Note    string      `json:"note"`
	Players []topPlayer `json:"players"`
}

// buildTopPlayers ranks every player by FPL form (average points over the
// last 30 days), then total points, then id, and keeps the first
// topPlayersCount. Flagged players stay in, with their status, since a
// reader browsing form wants to see who is in form but injured.
func buildTopPlayers(b *fpl.Bootstrap) topPlayers {
	ranked := make([]*fpl.Player, 0, len(b.Elements))
	for i := range b.Elements {
		ranked = append(ranked, &b.Elements[i])
	}
	slices.SortFunc(ranked, func(x, y *fpl.Player) int {
		return cmp.Or(
			cmp.Compare(y.Form.Float(), x.Form.Float()),
			cmp.Compare(y.TotalPoints, x.TotalPoints),
			cmp.Compare(x.ID, y.ID),
		)
	})
	short := make(map[int]string, len(b.Teams))
	for _, t := range b.Teams {
		short[t.ID] = t.ShortName
	}
	out := topPlayers{
		Note:    fmt.Sprintf("The %d players with the highest FPL form (average points per match over the last 30 days), ties broken by total points. status is a (available), d (doubtful), i (injured), s (suspended), u (unavailable) or n (not in the squad). Use player_comparison for detail.", topPlayersCount),
		Players: make([]topPlayer, 0, topPlayersCount),
	}
	for _, p := range ranked[:min(topPlayersCount, len(ranked))] {
		status := p.Status
		if status == "" {
			status = "a"
		}
		out.Players = append(out.Players, topPlayer{
			ID: p.ID, Name: p.WebName, Team: short[p.Team], Position: algo.Position(p.ElementType),
			Cost: float64(p.NowCost) / 10, Form: p.Form.Float(), TotalPoints: p.TotalPoints,
			PointsPerGame: p.PointsPerGame.Float(), OwnershipPct: p.SelectedByPercent.Float(), Status: status,
		})
	}
	return out
}

// addBrowsingResources registers fpl://fixtures/upcoming and
// fpl://players/top.
func addBrowsingResources(s *mcp.Server, c *fpl.Client) {
	s.AddResource(&mcp.Resource{
		URI: upcomingFixturesURI, Name: "upcoming_fixtures", Title: "Upcoming fixtures",
		Description: fmt.Sprintf("Every Premier League fixture in the next %d gameweeks, with kickoff time and FPL's difficulty rating (FDR 1-5) for each side.", upcomingGameweeks),
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		b, err := c.Bootstrap(ctx)
		if err != nil {
			return nil, err
		}
		fixtures, err := c.Fixtures(ctx)
		if err != nil {
			return nil, err
		}
		return jsonResource(upcomingFixturesURI, buildUpcomingFixtures(b, fixtures))
	})
	s.AddResource(&mcp.Resource{
		URI: topPlayersURI, Name: "top_players", Title: "Top players by form",
		Description: fmt.Sprintf("The %d players in the best FPL form, with team, position, price, form, total points, ownership and availability.", topPlayersCount),
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		b, err := c.Bootstrap(ctx)
		if err != nil {
			return nil, err
		}
		return jsonResource(topPlayersURI, buildTopPlayers(b))
	})
}

func jsonResource(uri string, v any) (*mcp.ReadResourceResult, error) {
	text, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(text)}}}, nil
}

// ---------------------------------------------------------------------------
// Prompts
// ---------------------------------------------------------------------------

var leagueIDArg = &mcp.PromptArgument{
	Name:        "league_id",
	Title:       "Mini-league ID",
	Description: "Your mini-league's ID: the number after /leagues/ in the league's standings URL, e.g. 123456 in fantasy.premierleague.com/leagues/123456/standings/c.",
	Required:    true,
}

// addPlanningPrompts registers one prompt each for chip_strategy,
// rival_tracker, fixture_outlook, optimal_transfers and player_comparison
// (#27). Each argument is substituted as given, with no conditional text,
// so mcpb/manifest.json can declare the same text with ${arguments.x}
// placeholders and TestManifestMatchesServer can hold the two to it.
func addPlanningPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "plan_my_chips",
		Title:       "Plan my chips",
		Description: "Find the best gameweek for each chip you have left, with how confident the call is.",
		Arguments:   []*mcp.PromptArgument{teamIDArg},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return userMessage(fmt.Sprintf(
			"Use the chip_strategy tool with team_id %s to plan my remaining chips. Then tell me:\n"+
				"1. For each chip I still have, the recommended gameweek and why (doubles, blanks, fixtures, my squad's fit)\n"+
				"2. How confident each call is, and the next-best gameweek if it's close\n"+
				"3. Any chip I should hold back because a better window is likely later in the season\n"+
				"4. What I should do with my squad in the run-up so each chip pays off\n"+
				"Finish with a simple chip calendar for the rest of this half of the season.",
			req.Params.Arguments["team_id"])), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "beat_my_rivals",
		Title:       "Beat my mini-league rivals",
		Description: "Compare your squad with the managers around you in a mini-league and find where to gain ground.",
		Arguments:   []*mcp.PromptArgument{teamIDArg, leagueIDArg},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return userMessage(fmt.Sprintf(
			"Use the rival_tracker tool with team_id %s and league_id %s to compare my squad with my closest rivals. Then tell me:\n"+
				"1. The points gap to each rival and how it has moved recently\n"+
				"2. The players they own that I don't (their threats) and the ones I own that they don't (my differentials)\n"+
				"3. The chips each rival has left compared with mine\n"+
				"4. Whether I should cover a big threat or back a differential this gameweek\n"+
				"Finish with the single move most likely to gain me ground this gameweek.",
			req.Params.Arguments["team_id"], req.Params.Arguments["league_id"])), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "fixture_swings",
		Title:       "Find fixture swings",
		Description: "Rank the teams by upcoming fixture difficulty and find the ones to target or avoid.",
		Arguments: []*mcp.PromptArgument{{
			Name:        "gameweeks_ahead",
			Title:       "Gameweeks ahead",
			Description: "How many gameweeks to look ahead, from 1 to 10. Defaults to 5.",
		}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		n := req.Params.Arguments["gameweeks_ahead"]
		if n == "" {
			n = "5"
		}
		return userMessage(fmt.Sprintf(
			"Use the fixture_outlook tool with gameweeks_ahead %s to rank every team by upcoming fixture difficulty. Then tell me:\n"+
				"1. The three teams with the best run, and which of their players are worth owning\n"+
				"2. The three teams with the worst run, and which popular players to consider selling\n"+
				"3. Any doubles or blanks in the window\n"+
				"4. Where the fixtures swing, so I know when to move in or out\n"+
				"Keep it practical: name players, not just teams.",
			n)), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "find_optimal_transfers",
		Title:       "Find my optimal transfers",
		Description: "Run the exact optimizer on your squad and weigh each transfer plan against its hit cost.",
		Arguments:   []*mcp.PromptArgument{teamIDArg},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return userMessage(fmt.Sprintf(
			"Use the optimal_transfers tool with team_id %s and allow_hits true to find the best transfers for my squad. Then tell me:\n"+
				"1. The best plan on free transfers alone, with the projected gain\n"+
				"2. Whether any plan with a hit beats it once the -4 per extra transfer is counted\n"+
				"3. The confidence of each plan and which players make it riskier\n"+
				"4. Any price-change risk on the players going out or coming in\n"+
				"Finish with exactly which transfers to make, or to roll the transfer if nothing is worth it.",
			req.Params.Arguments["team_id"])), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "compare_players",
		Title:       "Compare players",
		Description: "Head-to-head comparison of 2-4 players, including how consistently each one scores.",
		Arguments: []*mcp.PromptArgument{{
			Name:        "players",
			Title:       "Players",
			Description: "Two to four player names separated by commas, e.g. Haaland, Watkins, Isak. Partial names work.",
			Required:    true,
		}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return userMessage(fmt.Sprintf(
			"Use the player_comparison tool to compare these players: %s. Pass each name as a separate entry in player_names, and set include_gameweeks to 6. Then tell me:\n"+
				"1. Who is the better pick for the next few gameweeks, and why\n"+
				"2. How each one has scored: steady returns or a few big hauls\n"+
				"3. Whether their underlying numbers (xG, xA, minutes) back up their points\n"+
				"4. Fixtures, price and any injury or rotation risk\n"+
				"Finish with a clear verdict and when the other player would be the better choice.",
			req.Params.Arguments["players"])), nil
	})
}
