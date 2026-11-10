The hub no longer treats FPL's `ep_next` as a forecast. The single-swap tools now respect the 3-per-club rule, season totals match FPL's, and league win probabilities come from a documented simulation over every manager. You can now save your team ID and mini-leagues once, and sign in to FPL to read your squad as the app shows it. New tools: `pick_xi`, `manager_history` and `my_team`. Also new: transfer returns net of hits, chip confidence you can compare across chips, a signal calibration table, and five more prompts. Some responses gain fields and none lose any. `ep_next` and `chip_strategy`'s `confidence_score` are deprecated.

## Fixed

- **`ep_next` is not a projection** ([#42](https://github.com/fantasypl/mcp/issues/42), [#55](https://github.com/fantasypl/mcp/pull/55)): in 2026-27 FPL sets `ep_next` equal to `form` for every available player. The hub's bench order and captain tiebreak now use `projected_points`: season points per game, scaled by starts share, fixture difficulty, home or away, and chance of playing. `captain_pick` no longer says "FPL predicts 7.2pts", and `squad_scout` labels its `ep_next` figures as recent form.
- **3-per-club limit** ([#43](https://github.com/fantasypl/mcp/issues/43)): `transfer_suggestions` no longer offers a fourth player from one club. It flags options that break the limit when combined with earlier suggestions (`club_limit_ok`). The hub flags differentials from a full club and ranks them last. `player_comparison` takes `team_id` and leaves illegal buys out of its verdict.
- **Net season totals** ([#44](https://github.com/fantasypl/mcp/issues/44)): `season_summary.total_points` and `avg_points_per_gw` are net of hits, matching `manager_status`. `gross_points`, `transfer_cost_total` and `gameweek_breakdown` show the hits.
- **Fixture window** ([#45](https://github.com/fantasypl/mcp/issues/45)): `fixture_outlook` starts at the next gameweek, not a finished one, and reports `start_gameweek`.
- **FDR in reasoning** ([#46](https://github.com/fantasypl/mcp/issues/46)): one decimal, so a 1.8 fixture no longer reads "FDR 1".
- **League win probabilities** ([#47](https://github.com/fantasypl/mcp/issues/47)): a Monte Carlo of the rest of the season covering every manager on the first standings page (up to 50), with the method in `win_probability_method`. In the issue's league, the leader is 89 points clear with 28 gameweeks to play. The old estimate gave them 14.4%; the simulation gives 86.3%, and luck alone would give 39%. `team_id` adds your gap to the leader and the points per gameweek needed to close it.
- **Chip window** ([#54](https://github.com/fantasypl/mcp/issues/54)): `chip_strategy` stops at GW19 while first-half chips are in play. From GW11 it used to run to GW20, after they expire.
- **List cache hints** ([#27](https://github.com/fantasypl/mcp/issues/27), [#70](https://github.com/fantasypl/mcp/pull/70)): `tools/list`, `prompts/list`, `resources/list` and `resources/templates/list` now carry a one-hour `ttlMs` instead of go-sdk's default 0, which the spec defines as "immediately stale". This is the leading suspect for Claude Desktop's missing "Add from FPL Intelligence" menu entry.

## Added

- **Saved team ID and mini-leagues** ([#71](https://github.com/fantasypl/mcp/pull/71)): set `FPL_MCP_TEAM_ID` once and `team_id` becomes optional everywhere. `FPL_MCP_LEAGUES` takes up to five leagues (`314159, 271828:Work`). With leagues saved, the hub reports your rank, the gap to the leader, your win probability, threats and differentials in each league. The captain and transfer tools show each player's ownership among your rivals as `league_ownership`, with a `stance` of shield, template, differential or mixed. This is information only and changes no ranking.
- **Optional FPL login and `my_team`** ([#26](https://github.com/fantasypl/mcp/issues/26), [#71](https://github.com/fantasypl/mcp/pull/71)): off by default. With `FPL_MCP_LOGIN` set, `my_team` shows your squad as the FPL app does, with pending transfers applied. It also shows FPL's own purchase and selling prices, bank, free transfers and chip status. Budget tools use FPL's figures for your own team. The login is an OpenID Connect refresh token, kept in a 0600 file in your cache directory. The README shows how to copy it.
- **`pick_xi`** ([#51](https://github.com/fantasypl/mcp/issues/51)): picks the XI, formation, bench order and captain for your own 15. It shows the component scores behind each pick, the two nearest alternative formations, and fixture exposure. Form only breaks ties.
- **`manager_history`** ([#49](https://github.com/fantasypl/mcp/issues/49)): every gameweek's points, hits, bench points, ranks, chip, captain and auto-subs. It also names the best captain in your XI and the points you forwent.
- **Transfer returns** ([#50](https://github.com/fantasypl/mcp/issues/50)): `manager_transfer_history` shows each transfer's points gained or lost, net of any hit, and how long you held the player. It also says whether a Wildcard sold him, and adds totals and a per-gameweek roll-up.
- **Chosen rivals** ([#53](https://github.com/fantasypl/mcp/issues/53)): `rival_tracker` takes `rival_team_ids` and `target_ranks`, and every manager gains gameweek points and chips used. In `league_analyzer`, your row lists the gap to every other manager.
- **Chip confidence** ([#54](https://github.com/fantasypl/mcp/issues/54)): `confidence` can now be compared across chips. Each chip also lists the next-best gameweek and the gap to it, and the top five candidates with their factors. You also get scheduled doubles and blanks beyond the window, and rivals' remaining chips when you pass `league_id`.
- **`fpl://calibration`** ([#52](https://github.com/fantasypl/mcp/issues/52)): how well each signal has ranked next-gameweek points this season, by position, with sample sizes. The new `fplctl calibrate` writes it.
- **`player_comparison` upgrades** ([#24](https://github.com/fantasypl/mcp/issues/24), [#25](https://github.com/fantasypl/mcp/issues/25), [#70](https://github.com/fantasypl/mcp/pull/70)): `fields` returns any raw FPL element field. `include_gameweeks` returns the last N gameweeks per player, with a `consistency` profile: `steady`, `haul-dependent`, `mixed` or `low output`.
- **Five more prompts and two resources** ([#27](https://github.com/fantasypl/mcp/issues/27)): the prompts are `plan_my_chips`, `beat_my_rivals`, `fixture_swings`, `find_optimal_transfers` and `compare_players`. The resources are `fpl://fixtures/upcoming` and `fpl://players/top`.

## Changed

- `transfer_suggestions` can return fewer options, and the hub's differential order can change (#43).
- `league_analyzer`'s `win_probability` values change. A manager whose squad couldn't be fetched now gets a probability instead of 0 (#47).
- `chip_strategy`'s `scan_window` is shorter between GW11 and GW19 (#54).

## Deprecated

- `ep_next` (hub, `captain_pick`, `player_comparison`, `squad_scout`): unchanged in this release and removed in the next. Use `projected_points`.
- `chip_strategy`'s `confidence_score`: equal to `chip_score` in this release and removed in the next. Use `chip_score` and `confidence`.

## Upgrade notes

- If a prompt or script reads `ep_next` as a forecast, switch it to `projected_points` now.
- If you compare `chip_strategy` figures across chips, use `confidence`. `chip_score` is only comparable between gameweeks for the same chip.
- `fpl://calibration` reads a file. Run `fplctl calibrate` after each gameweek to keep it current. It writes to the fpl-mcp cache directory, or to `FPL_MCP_CALIBRATION_FILE` if set. Until a file exists, the resource says how to make one.
- If you took hits, the hub's season totals drop by your total hit cost. They now match FPL.
- `.mcpb` users can set "Your FPL team ID", "Your mini-leagues" and "FPL login" in the extension's settings. All three are optional.

## Known limitations

- **FPL login** is tested against a fake OpenID provider and fake FPL endpoints, not a live FPL account. An expired login makes `my_team` return a clear error. Other tools fall back to public data.
- **The missing prompt menu in Claude Desktop** (#27) is not yet confirmed fixed. Install this build and check the + menu.
- **Injury status disagreeing with the app** ([#48](https://github.com/fantasypl/mcp/issues/48)) is not reproduced and stays open. Cached responses can be up to about 10 minutes old, or an hour while FPL's origin is returning errors.
- **`captain_score` still weights `ep_next`**, so it counts form twice in 2026-27. Fixing it changes every captain ranking and needs its own backtest.
- **League win probabilities** rest on a judgement call: each manager's scoring record is blended with 10 gameweeks of league average. More blending pulls leaders toward the luck-only figure. The model ignores squads, fixtures and injuries.
- **Saved leagues** add time to the hub: 6.1s cold and 1.8s warm with three leagues, against 1.0s without.
- **Calibration** uses the vaastav corpus, which currently ends at GW6 of 2026-27, so the table runs behind the live season.
- **`pick_xi`** uses each player's season-to-date starts share, not recent starts.

## Install

Download the `.mcpb` bundle for your platform below and install it in Claude Desktop, or run:

```bash
go install github.com/fantasypl/mcp/cmd/fpl-mcp@v0.3.0
```

See [README.md](https://github.com/fantasypl/mcp/blob/main/README.md) for setup and [CHANGELOG.md](https://github.com/fantasypl/mcp/blob/main/CHANGELOG.md) for the complete history.
