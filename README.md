# FPL Intelligence

An MCP server for Fantasy Premier League analysis: captain picks, transfer suggestions, fixture difficulty, differentials, price predictions, mini-league tracking, and a full manager intelligence report — all backed by live data from the official FPL API.

A single static Go binary. No API keys, no hosted service, no per-request cost — each user runs their own copy against the free, public FPL API.

## Install

```bash
go install github.com/fantasypl/mcp/cmd/fpl-mcp@latest
```

This installs a `fpl-mcp` binary to `$(go env GOPATH)/bin`. Add that to your `PATH`, or reference the binary by its full path in your MCP client config.

## Claude Desktop setup

Add to `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "fpl-intelligence": {
      "command": "fpl-mcp"
    }
  }
}
```

Or with a full path if `fpl-mcp` isn't on your `PATH`:

```json
{
  "mcpServers": {
    "fpl-intelligence": {
      "command": "/absolute/path/to/fpl-mcp"
    }
  }
}
```

## Tools

| Tool | Description |
|---|---|
| `fpl_manager_hub` | Complete intelligence report for a manager's team, with the next deadline and time remaining — the best starting point. Each squad player has `projected_points`, a next-gameweek estimate that drives the suggested bench order and captain tiebreak. Season totals are net of hits, with the gross figure and hit cost alongside |
| `captain_pick` | Top 5 captain recommendations for a gameweek, from every player or from one squad given by `team_id` or `player_ids` |
| `pick_xi` | Starting XI, formation, bench order and captain for an existing 15, with each player's component scores, the two nearest alternative formations, and fixture exposure |
| `differential_finder` | Underowned players outperforming their ownership |
| `fixture_outlook` | Teams ranked by upcoming fixture difficulty, from the next gameweek on |
| `price_predictions` | Likely price rises and falls tonight |
| `transfer_suggestions` | Transfer recommendations for a team. Every option keeps the 3-per-club limit, and `club_limit_ok` flags options that break it together with earlier suggestions |
| `optimal_squad` | The highest-projected-points 15-man squad achievable under a budget, via an exact combinatorial optimizer |
| `optimal_transfers` | Combinatorially optimal transfers for a team's current squad, weighed against hit cost. Set `wildcard` for the best squad reachable on a Wildcard, budgeted at bank plus selling prices |
| `player_comparison` | Head-to-head comparison of 2-4 players. With `team_id`, each player gains `legality` (3-per-club and budget), and illegal buys are left out of the verdict. `fields` passes through any bootstrap element field per player; `include_gameweeks` adds the last N finished gameweeks and a consistency measure (steady vs haul-dependent) |
| `live_points` | Live points during an active gameweek |
| `manager_history` | A manager's season gameweek by gameweek: points before and after hits, bench points, ranks, chips, captain and auto-subs, and the best captain in the XI with the points forgone |
| `is_hit_worth_it` | Whether a -4 transfer hit is worth taking |
| `chip_strategy` | When to use each remaining chip |
| `rival_tracker` | Mini-league rival analysis and counter-strategies, against your neighbours or rivals you choose (`rival_team_ids`, `target_ranks`), with each manager's gameweek points and chips played |
| `league_analyzer` | Win probabilities for every manager on a mini-league's first standings page, from a Monte Carlo of the rest of the season. Optional `team_id` adds the caller's gap to the leader and to every other manager, with the points per gameweek needed to close each |
| `squad_scout` | Deep scout report using FPL's less-visible data fields. Its `ep_next` figures are FPL's own; in 2026-27 FPL sets `ep_next` equal to form, so read them as recent form |
| `manager_transfer_history` | A manager's transfers this season, with each one's points gained or lost net of any hit, and the purchase, market and selling price of each current squad player |
| `my_team` | Your team as it stands in the FPL app before the deadline: pending transfers applied, FPL's own purchase and selling prices, bank, free transfers and chip state. Needs the optional FPL login below |

Plus five resources:

| Resource | Contents |
|---|---|
| `fpl://status` | Current and next gameweek state, the next deadline in UTC with time remaining, and season progress |
| `fpl://teams` | All 20 Premier League teams |
| `fpl://fixtures/upcoming` | Every fixture in the next 5 gameweeks, with kickoff time and FPL's FDR for each side |
| `fpl://players/top` | The 50 players in the best FPL form, with team, position, price, ownership and availability |
| `fpl://calibration` | How well each signal has ranked next-gameweek points this season; see below |

And ten pre-built prompts for Claude Desktop's prompt selector: `analyze_my_fpl_team`, `who_should_i_captain`, `find_differential_picks`, `plan_my_transfers`, `price_change_alert`, `plan_my_chips`, `beat_my_rivals`, `fixture_swings`, `find_optimal_transfers` and `compare_players`.

### Your team and mini-leagues

Save your own team ID and the mini-leagues you compete in, and the server stops asking for them and weighs advice against the managers you're actually playing.

- **Your FPL team ID** (`my_team_id` in the extension settings, or `FPL_MCP_TEAM_ID`) is the number after `/entry/` in your FPL points page URL. Every tool that takes `team_id` uses it when a call leaves `team_id` out. With the FPL login on and no team ID saved, the logged-in team is used.
- **Your mini-leagues** (`my_leagues`, or `FPL_MCP_LEAGUES`) are up to five league IDs, the number after `/leagues/` in each standings URL, separated by commas. A pasted standings URL works too. Add a colon and a name to label one:

  <!-- fpl-leagues-example -->
  ```text
  314159, 271828:Work
  ```

With leagues saved:

- `fpl_manager_hub` gains `leagues`, one entry per league with your rank, the points to the leader and to the places above and below, your win probability (the same simulation as `league_analyzer`), your captain's ownership among that league's rivals with a note when most of them captained someone else, `threats` (players at least half your rivals own and you don't) and `differentials` (your starters 15% or fewer of them own). Each squad player gains `league_ownership`.
- `captain_pick`, `transfer_suggestions` and `optimal_transfers` (including Wildcard mode) add `league_ownership` to each player: how many of your rivals own and captained him, effective ownership, and a `stance` of shield, template, differential or mixed. `league_ownership_basis` in each result defines these. It is information and changes no ranking.
- `rival_tracker`, `league_analyzer` and `chip_strategy` use the first saved league when `league_id` is left out.

Rivals are every manager in a league of 20 or fewer, or the 10 places either side of you in a bigger one, at most 60 across all leagues and each counted once. Their squads are the latest public picks, so transfers made since the last deadline don't show until the next one passes. Each saved league adds a standings request and up to 20 squad requests to the tools above, all cached for two minutes.

### Optional FPL login

Every tool works from FPL's public data, and the login is off by default. With it, `my_team` can show your team as it stands in the FPL app before you confirm it: pending transfers, a draft Wildcard, captain changes, exact selling prices and chip state. Other tools then also use FPL's exact selling prices for your own team, marked `purchase_source: fpl_my_team`.

FPL signs you in through an OpenID Connect provider and keeps the session in your browser's local storage. To set it up:

1. Log in at https://fantasy.premierleague.com.
2. Open the browser console (F12, then Console) and run:

   ```js
   copy((k => JSON.stringify({authority: k.split(':').slice(1, -1).join(':'), client_id: k.split(':').pop(), refresh_token: JSON.parse(localStorage[k]).refresh_token}))(Object.keys(localStorage).find(k => k.startsWith('oidc.user:'))))
   ```

   Chrome may ask you to type `allow pasting` first. The snippet copies one line of JSON to your clipboard, shaped like this:

   <!-- fpl-login-example -->
   ```json
   {"authority":"https://account.premierleague.com/as","client_id":"<client id>","refresh_token":"<refresh token>"}
   ```

3. Paste it into the extension's "FPL login" setting, or set it as `FPL_MCP_LOGIN` in your MCP client config.

How the server handles it:

- **Nothing is built in.** The authority and client ID come from your paste, and the token endpoint from the authority's OpenID discovery document. The authority, the issuer it reports and the token endpoint must all be https URLs on premierleague.com, so the token cannot be sent anywhere else.
- **Your browser session ends.** The provider replaces the refresh token each time it is used, so the copy in your browser stops working and the site asks you to log in again. The server keeps the newest refresh token in `fpl-mcp/auth/session.json` under your user cache directory, readable only by you.
- **No token is logged** or returned in tool output, and logged-in requests never follow a redirect.
- **An expired or signed-out login** makes `my_team` return an error saying so. Copy a fresh login and paste it again; the server notices the new token and starts from it. Other tools fall back to selling prices computed from public data.

### Signal calibration

`fpl://calibration` reports the Spearman rank correlation between each signal (form, points per game, `captain_score`, xGI per 90, minutes share, fixture difficulty) and the points players scored the following gameweek, overall and for DEF, MID and FWD, with sample sizes and 95% intervals. The server doesn't compute it, because rebuilding every gameweek takes too long for a resource read. `fplctl calibrate` writes it to `calibration.json` in the fpl-mcp cache directory, where the server reads it (or set `FPL_MCP_CALIBRATION_FILE`). Re-run it after each gameweek. It uses the vaastav corpus, which publishes each gameweek's results some time after it ends.

### A note on `ep_next`

FPL's `ep_next` field is labelled "expected points", but in 2026-27 FPL sets it equal to `form` for every available player and to 0 for most flagged ones. This server no longer treats it as a forecast. Tools that rank players for the next gameweek use `projected_points` instead: season points per game, scaled by the share of team games the player started, FPL fixture difficulty, home or away, and chance of playing. `ep_next` stays in tool output, unchanged, for one more release and is deprecated.

## fplctl

The operational counterpart, for maintaining the weight-tuning and backtest pipeline:

```bash
fplctl snapshot [--gw N] [--backfill] [--root DIR]
fplctl optimize [--window N] [--root DIR]
fplctl backtest [--gw N | --dir DIR --from N --to N | --seasons S1,S2,... --from N --to N [--holdout SEASON] [--elo | --minutes | --congestion]] [--top N] [--root DIR]
fplctl evaluate --gw N [--root DIR]
fplctl audit [--team-id N] [--root DIR]
fplctl gengolden [--which SET] [--out DIR] | --check
fplctl finishing-regression [--season S] [--split-gw N] [--to N] [--group-size N] [--root DIR]
fplctl role-change [--season S] [--baseline-from N] [--baseline-to N] [--recent-from N] [--split-gw N] [--to N] [--group-size N] [--min-matches N] [--min-avg-minutes F] [--exclude-gk] [--root DIR]
fplctl elo-capture [--root DIR]
fplctl congestion-capture [--root DIR]
fplctl calibrate [--season S] [--from N] [--to N] [--out FILE] [--root DIR]
```

### Chip confidence

`chip_strategy`'s `chip_score` is in each chip's own units, so compare it only between gameweeks for the same chip. `confidence` (high, medium or low) is comparable across chips: it is the recommended gameweek's margin over that chip's next-best gameweek. Each recommendation also lists `next_best_gameweek`, `gap_to_next` and its top five `candidates` with the factors behind them. Pass `league_id` to see rivals' remaining chips.

## Development

```bash
make build   # build both binaries into bin/
make test    # go test ./... -race
make lint    # golangci-lint
```

`fplctl gengolden --check` verifies the committed golden files under `testdata/` still match a fresh regeneration — run it before trusting any `fplctl gengolden` write, since those fixtures are frozen, point-in-time FPL API payloads that can't be refetched once a gameweek has passed.

## License

MIT — see [LICENSE](LICENSE).
