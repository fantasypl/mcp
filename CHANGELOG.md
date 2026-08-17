# Changelog

All notable changes to this project are documented here.

## [Unreleased]

### Added

- MCP server (`cmd/fpl-mcp`) exposing 13 tools, 2 resources, and 5 prompts for Fantasy Premier League analysis: captain picks, differentials, fixture outlook, price predictions, transfer suggestions, player comparison, live points, hit analysis, chip strategy, rival tracking, league analysis, squad scouting, and a full manager intelligence report (`fpl_manager_hub`).
- `fpl_manager_hub` auto-detects a manager's bank, free transfers, and chip status, then runs every other team-scoped tool in parallel against the same gameweek for one coherent report.
- `fplctl`, the operational counterpart: gameweek snapshots, weight optimization, backtesting, accuracy evaluation, data-integrity auditing, and golden-fixture regeneration (`gengolden`).
- `fplctl gengolden --check` verifies committed test fixtures still match a fresh regeneration without writing anything, guarding the frozen, point-in-time FPL API payloads under `testdata/` against silent drift.
- CI: build, vet, race-enabled tests, golden-fixture check, and lint on every push and PR.
- Cross-platform release builds (Linux/macOS/Windows, amd64/arm64) attached to GitHub Releases.
- `internal/vaastav`: reconstructs point-in-time FPL state from vaastav/Fantasy-Premier-League's per-gameweek CSVs (season-to-date totals with no look-ahead), fetched on demand and cached locally — replaces `scripts/backtest_from_vaastav.py`.
- `fplctl backtest -seasons S1,S2,... [-holdout SEASON]`: runs the captain-pick algorithm across many vaastav seasons at once, reporting a held-out season's accuracy separately from the seasons used for tuning, to guard against overfitting when weights or scoring are changed.
- `internal/insights`: selective fetch, TTL-cache (12h, matching the source's twice-daily refresh), and CSV decode for olbauday/FPL-Core-Insights — current-season Elo, per-match minutes, and other detail data. Verified live that its `player_id`/team `id` already match FPL's own ids directly, so joining is a plain map lookup with a typed miss (`LookupID`'s second return), never a fuzzy match. Missing files (a gameweek not yet played, or the enrichment tier lagging a new season — verified live that 2026-27 currently ships only the base files) surface as `ErrNotAvailable` rather than an error, so callers can degrade to FPL-API-only behavior.
- `internal/clubelo`: client for clubelo.com's historical Elo (needed to back-test an Elo-driven fixture multiplier across past seasons — FPL-Core-Insights' `teams.csv:elo` only carries a current snapshot). The formerly-keyless CSV API at `api.clubelo.com` is unreachable from this environment (both HTTP and HTTPS hang outbound — looks like a network-egress restriction, not the service being down), so this scrapes clubelo.com's public club pages instead: the current-Elo header line, plus a recent Elo history (~4 seasons as of writing) from an embedded Vega-Lite chart JSON blob, which is a real structured payload rather than table-cell scraping. Team identity (`SlugFor`) is a verified table, not a guess — every slug was read from a real href on the live `https://clubelo.com/ENG` page, including newly-promoted Sunderland. Smoke-tested end-to-end against the live site for five clubs. The table also covers five clubs relegated before 2026-27 (Leicester, Southampton, Luton, Sheffield United, Ipswich), each verified individually, so historical seasons back-test correctly.
- `fplctl backtest -seasons ... -elo`: runs the unmodified captain algorithm twice per gameweek — once against FPL's own dynamic team strength, once with an Elo-derived value substituted or blended in (`cmd/fplctl/elo_backtest.go`) — and reports both, split by tuning vs. held-out season. Kept as reusable measurement tooling regardless of the result below.

### Measured and rejected

- **Elo-driven fixture multiplier does not ship.** Part B's plan ranked replacing FDR with continuous ClubElo ratings as the single highest-leverage change available, but flagged that the production captain algorithm already blends FDR with FPL's own dynamic team-strength ratings (`blendFDR` in `internal/algo/captain.go`) — a step beyond the plan's original assumption of pure-FDR scoring — and asked that Elo be measured against that blend, not assumed to win. It doesn't: backtested via `fplctl backtest -seasons 2022-23,2023-24,2024-25,2025-26 -holdout 2025-26 -elo -from 2 -to 38` (109 tuning gameweeks, 37 held-out), both an Elo-substitutes-FPL-strength variant and an Elo-averaged-with-FPL-strength variant scored *worse* than the current baseline on the algorithm's #1 captain pick, consistently across every tuning/held-out/variant combination:

  | | Baseline | Elo (replace) | Elo (blend) |
  |---|---:|---:|---:|
  | Tuning (109 GW): #1 pick total pts | 729 | 706 | 723 |
  | Tuning: #1 pick top-10 finishes | 24/109 | 21/109 | 22/109 |
  | Held-out 2025-26 (37 GW): #1 pick total pts | 210 | 204 | 204 |
  | Held-out: #1 pick top-10 finishes | 6/37 | 6/37 | 6/37 |

  `internal/algo/captain.go` is unchanged as a result — per Part B's rule, a change that doesn't improve out-of-sample accuracy doesn't ship. The measurement harness (`cmd/fplctl/elo_backtest.go`, `fplctl backtest -elo`) stays in the tree in case a different blend design or better Elo coverage changes this later; re-running it is one command.

### Fixed

- Re-baselined `testdata/rivals_scenario/{bootstrap,fixtures,golden}.json` to a fresh `fplctl gengolden -which rivals` regeneration, closing the drift `gengolden --check` had been deferring since `cec55bf` (that commit re-baselined `chips_scenario`/`league_scenario` but deliberately left `rivals_scenario` for its own review). No `internal/algo` scoring or ranking code changed — `formatPlayerList`'s differentials tie-break (form descending, then player ID ascending via a stable sort) was already deterministic and correct. The drift's real cause: `genRivals` derives its bootstrap/fixtures by reading `testdata/league_scenario`'s committed files, and those were themselves re-baselined to the Go-struct-modeled field set in `cec55bf` — `rivals_scenario`'s own files were never resynced afterward, so they'd frozen on a stale, richer player universe. Regenerating just catches `rivals_scenario` up to the input data its own generator has been using all along; the visibly different differentials (different players, costs, teams) reflect that different-but-already-established input, not a scoring change. `gengolden --check` is now fully clean, and the now-empty `knownGoldenDrift` deferred-drift mechanism was removed from `cmd/fplctl/gengolden.go`.

