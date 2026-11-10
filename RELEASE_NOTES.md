The hub stops treating FPL's `ep_next` as a forecast, the single-swap tools respect the 3-per-club rule, season totals match FPL's, and league win probabilities are now a documented simulation over every manager. New: `pick_xi`, `manager_history`, transfer returns net of hits, chosen rivals, comparable chip confidence and a signal calibration table. Some responses gain fields; nothing is removed. `ep_next` and `chip_strategy`'s `confidence_score` are deprecated.

> **Draft.** These notes describe `main` after all 13 pull requests below are merged. None is merged yet. Merge them in this order; each merges cleanly onto the one before, and the combined result passes `go test ./... -race` and `fplctl gengolden --check`:
>
> 1. [#55](https://github.com/fantasypl/mcp/pull/55) (#42) · 2. [#59](https://github.com/fantasypl/mcp/pull/59) (#46) · 3. [#61](https://github.com/fantasypl/mcp/pull/61) (#51) · 4. [#56](https://github.com/fantasypl/mcp/pull/56) (#43) · 5. [#57](https://github.com/fantasypl/mcp/pull/57) (#44) · 6. [#60](https://github.com/fantasypl/mcp/pull/60) (#47) · 7. [#64](https://github.com/fantasypl/mcp/pull/64) (#53) · 8. [#62](https://github.com/fantasypl/mcp/pull/62) (#49) · 9. [#63](https://github.com/fantasypl/mcp/pull/63) (#50) · 10. [#58](https://github.com/fantasypl/mcp/pull/58) (#45) · 11. [#65](https://github.com/fantasypl/mcp/pull/65) (#54) · 12. [#66](https://github.com/fantasypl/mcp/pull/66) (#52) · 13. [#67](https://github.com/fantasypl/mcp/pull/67) (CHANGELOG and these notes)

## Fixed

- **`ep_next` is not a projection** ([#42](https://github.com/fantasypl/mcp/issues/42), [#55](https://github.com/fantasypl/mcp/pull/55)): in 2026-27 FPL sets `ep_next` equal to `form` for every available player. The hub's bench order and captain tiebreak now use `projected_points`: season points per game, scaled by starts share, fixture difficulty, home or away, and chance of playing. `captain_pick` no longer says "FPL predicts 7.2pts", and `squad_scout` labels its `ep_next` figures as recent form.
- **3-per-club limit** ([#43](https://github.com/fantasypl/mcp/issues/43), [#56](https://github.com/fantasypl/mcp/pull/56)): `transfer_suggestions` no longer offers a fourth player from one club, and flags options that break the limit together with earlier suggestions (`club_limit_ok`). The hub flags full-club differentials and ranks them last. `player_comparison` takes `team_id` and leaves illegal buys out of its verdict.
- **Net season totals** ([#44](https://github.com/fantasypl/mcp/issues/44), [#57](https://github.com/fantasypl/mcp/pull/57)): `season_summary.total_points` and `avg_points_per_gw` are net of hits, matching `manager_status`. `gross_points`, `transfer_cost_total` and `gameweek_breakdown` show the hits.
- **Fixture window** ([#45](https://github.com/fantasypl/mcp/issues/45), [#58](https://github.com/fantasypl/mcp/pull/58)): `fixture_outlook` starts at the next gameweek, not a finished one, and reports `start_gameweek`.
- **FDR in reasoning** ([#46](https://github.com/fantasypl/mcp/issues/46), [#59](https://github.com/fantasypl/mcp/pull/59)): one decimal, so a 1.8 fixture no longer reads "FDR 1".
- **League win probabilities** ([#47](https://github.com/fantasypl/mcp/issues/47), [#60](https://github.com/fantasypl/mcp/pull/60)): a Monte Carlo of the rest of the season covering every manager on the first standings page (up to 50), with the method in `win_probability_method`. In the issue's league, the leader, 89 points clear with 28 to play, goes from 14.4% to 86.3%; luck alone would give 39%. `team_id` adds the caller's gap and the points a gameweek needed to close it.
- **Chip window** ([#54](https://github.com/fantasypl/mcp/issues/54), [#65](https://github.com/fantasypl/mcp/pull/65)): `chip_strategy` stops at GW19 while first-half chips are in play. From GW11 it used to run to GW20, after they expire.

## Added

- **`pick_xi`** ([#51](https://github.com/fantasypl/mcp/issues/51), [#61](https://github.com/fantasypl/mcp/pull/61)): XI, formation, bench order and captain for your own 15, with the component scores behind each pick, the two nearest alternative formations, and fixture exposure. Form only breaks ties.
- **`manager_history`** ([#49](https://github.com/fantasypl/mcp/issues/49), [#62](https://github.com/fantasypl/mcp/pull/62)): every gameweek's points, hits, bench, ranks, chip, captain, auto-subs, and the best captain in your XI with the points forgone.
- **Transfer returns** ([#50](https://github.com/fantasypl/mcp/issues/50), [#63](https://github.com/fantasypl/mcp/pull/63)): `manager_transfer_history` shows each transfer's points gained or lost net of any hit, how long the player was held, and whether a Wildcard sold him, plus totals and a per-gameweek roll-up.
- **Chosen rivals** ([#53](https://github.com/fantasypl/mcp/issues/53), [#64](https://github.com/fantasypl/mcp/pull/64)): `rival_tracker` takes `rival_team_ids` and `target_ranks`, and every manager gains gameweek points and chips used. `league_analyzer`'s caller row lists the gap to every manager.
- **Chip confidence** ([#54](https://github.com/fantasypl/mcp/issues/54), [#65](https://github.com/fantasypl/mcp/pull/65)): `confidence` is comparable across chips, with the next-best gameweek, the gap to it, the top five candidates with their factors, scheduled doubles and blanks beyond the window, and rivals' chips with `league_id`.
- **`fpl://calibration`** ([#52](https://github.com/fantasypl/mcp/issues/52), [#66](https://github.com/fantasypl/mcp/pull/66)): how well each signal has ranked next-gameweek points this season, by position, with sample sizes, written by the new `fplctl calibrate`.

## Changed

- `transfer_suggestions` can return fewer options, and the hub's differential order can change (#43).
- `league_analyzer`'s `win_probability` values change, and a manager whose squad couldn't be fetched now gets a probability instead of 0 (#47).
- `chip_strategy`'s `scan_window` is shorter between GW11 and GW19 (#54).

## Deprecated

- `ep_next` (hub, `captain_pick`, `player_comparison`, `squad_scout`): unchanged in this release and removed in the next. Use `projected_points`.
- `chip_strategy`'s `confidence_score`: equal to `chip_score` in this release and removed in the next. Use `chip_score` and `confidence`.

## Upgrade notes

- If a prompt or script reads `ep_next` as a forecast, switch it to `projected_points` now.
- If you compare `chip_strategy` figures across chips, use `confidence`; `chip_score` is only comparable between gameweeks for the same chip.
- `fpl://calibration` reads a file. Run `fplctl calibrate` after each gameweek to keep it current. It writes to the fpl-mcp cache directory, or set `FPL_MCP_CALIBRATION_FILE`. Until then the resource says how to make one.
- Season totals in the hub drop by your total hit cost if you took hits. They now match FPL.

## Known limitations

- **Injury status disagreeing with the app** ([#48](https://github.com/fantasypl/mcp/issues/48)) is not reproduced and stays open. Today's data shows `status` and `chance_of_playing_next_round` consistent for every player, and the server passes `status` through unchanged. Cached responses can be up to about 10 minutes old, or an hour while FPL's origin is erroring. The issue has the full notes and a capture recipe for next time.
- **`captain_score` still weights `ep_next`**, and so counts form twice in 2026-27. Fixing it changes every captain ranking and needs its own backtest.
- **League win probabilities** depend on a judgement call: each manager's scoring record is blended with 10 gameweeks of league average. More blending moves leaders toward the luck-only figure. Squads, fixtures and injuries are not in the model.
- **Calibration** uses the vaastav corpus, which currently ends at GW6 of 2026-27, so the table runs behind the live season. `.mcpb` users get it only once someone runs `fplctl calibrate` for them.
- **`season_outlook`** in `chip_strategy` shows only doubles and blanks FPL has scheduled. Postponed fixtures without a gameweek appear under `pending_dgws` instead.
- **`pick_xi`** uses each player's season-to-date starts share, not recent starts.

## Install

Download the `.mcpb` bundle for your platform below and install it in Claude Desktop, or:

```bash
go install github.com/fantasypl/mcp/cmd/fpl-mcp@latest
```

See [README.md](https://github.com/fantasypl/mcp/blob/main/README.md) for setup and [CHANGELOG.md](https://github.com/fantasypl/mcp/blob/main/CHANGELOG.md) for the complete history.
