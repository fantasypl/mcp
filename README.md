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
| `fpl_manager_hub` | Complete intelligence report for a manager's team — the best starting point |
| `captain_pick` | Top 5 captain recommendations for a gameweek |
| `differential_finder` | Underowned players outperforming their ownership |
| `fixture_outlook` | Teams ranked by upcoming fixture difficulty |
| `price_predictions` | Likely price rises and falls tonight |
| `transfer_suggestions` | Transfer recommendations for a team |
| `player_comparison` | Head-to-head comparison of 2-4 players |
| `live_points` | Live points during an active gameweek |
| `is_hit_worth_it` | Whether a -4 transfer hit is worth taking |
| `chip_strategy` | When to use each remaining chip |
| `rival_tracker` | Mini-league rival analysis and counter-strategies |
| `league_analyzer` | Win probability predictions for a mini-league |
| `squad_scout` | Deep scout report using FPL's less-visible data fields |

Plus two resources — `fpl://status` (current gameweek and season progress) and `fpl://teams` (all 20 Premier League teams) — and five pre-built prompts that appear in Claude Desktop's prompt selector.

## fplctl

The operational counterpart, for maintaining the weight-tuning and backtest pipeline:

```bash
fplctl snapshot [--gw N] [--backfill] [--root DIR]
fplctl optimize [--window N] [--root DIR]
fplctl backtest [--gw N | --dir DIR --from N --to N | --seasons S1,S2,... --from N --to N [--holdout SEASON] [--elo]] [--top N] [--root DIR]
fplctl evaluate --gw N [--root DIR]
fplctl audit [--team-id N] [--root DIR]
fplctl gengolden [--which SET] [--out DIR] | --check
```

## Development

```bash
make build   # build both binaries into bin/
make test    # go test ./... -race
make lint    # golangci-lint
```

`fplctl gengolden --check` verifies the committed golden files under `testdata/` still match a fresh regeneration — run it before trusting any `fplctl gengolden` write, since those fixtures are frozen, point-in-time FPL API payloads that can't be refetched once a gameweek has passed.

## License

MIT — see [LICENSE](LICENSE).
