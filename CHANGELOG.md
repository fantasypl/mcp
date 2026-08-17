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

### Fixed

- Re-baselined `testdata/rivals_scenario/{bootstrap,fixtures,golden}.json` to a fresh `fplctl gengolden -which rivals` regeneration, closing the drift `gengolden --check` had been deferring since `cec55bf` (that commit re-baselined `chips_scenario`/`league_scenario` but deliberately left `rivals_scenario` for its own review). No `internal/algo` scoring or ranking code changed — `formatPlayerList`'s differentials tie-break (form descending, then player ID ascending via a stable sort) was already deterministic and correct. The drift's real cause: `genRivals` derives its bootstrap/fixtures by reading `testdata/league_scenario`'s committed files, and those were themselves re-baselined to the Go-struct-modeled field set in `cec55bf` — `rivals_scenario`'s own files were never resynced afterward, so they'd frozen on a stale, richer player universe. Regenerating just catches `rivals_scenario` up to the input data its own generator has been using all along; the visibly different differentials (different players, costs, teams) reflect that different-but-already-established input, not a scoring change. `gengolden --check` is now fully clean, and the now-empty `knownGoldenDrift` deferred-drift mechanism was removed from `cmd/fplctl/gengolden.go`.

