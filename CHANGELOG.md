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
