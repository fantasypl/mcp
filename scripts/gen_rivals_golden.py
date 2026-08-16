"""
Golden fixture for rivals.get_rival_analysis — reuses the 4-manager league
scenario's squads and standings (see gen_league_golden.py) but sets the
current gameweek to 10 rather than 1, specifically so the "recent transfers"
window (event >= current_gw - 2) actually excludes something: at GW1 that
filter is a no-op (nothing is before gameweek -1), so it needs a later
gameweek to be exercised at all.

The user is Sam Rival (999002, rank 2). With RIVAL_WINDOW=3 and only 4
managers, every other manager falls in the window, exercising:
  - Alex Chaser (999001): 8pts ahead of the user (point_gap=-8) — closest
    rival by absolute gap, gets a transfer prediction. Has a mix of old
    (GW3, filtered out) and recent (GW9/GW10, kept) transfers.
  - Jo Hoarder (999003): 152pts behind (point_gap=152) — second-closest of
    the *successfully fetched* rivals once Kim is excluded, also gets a
    prediction. Only old transfers, so recent_transfers ends up genuinely
    empty — exercising the "closest rival, zero recent transfers" case that
    ruled out plain slice omitempty (see RivalAnalysis.RecentTransfers).
  - Kim Failure (999004): squad fetch fails entirely, excluded from
    rival_analyses (not just flagged — actually dropped, unlike
    league_analyzer's per-manager error entries).

Usage: cd reference/python-src && uv run python ../../scripts/gen_rivals_golden.py
"""

import asyncio
import copy
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
TESTDATA = ROOT / "testdata" / "rivals_scenario"
REFERENCE = ROOT / "reference" / "python-src"
sys.path.insert(0, str(REFERENCE))

LEAGUE_ID = 12345
USER_TEAM_ID = 999002
GW = 10

LEAGUE_SRC = TESTDATA.parent / "league_scenario"
BOOTSTRAP = json.loads((LEAGUE_SRC / "bootstrap.json").read_text())
FIXTURES = json.loads((LEAGUE_SRC / "fixtures.json").read_text())
STANDINGS = json.loads((LEAGUE_SRC / "standings.json").read_text())
PICKS_A = json.loads((LEAGUE_SRC / "picks_a.json").read_text())  # 999001 Alex
PICKS_B = json.loads((LEAGUE_SRC / "picks_b.json").read_text())  # 999002 Sam (the user)
PICKS_C = json.loads((LEAGUE_SRC / "picks_c.json").read_text())  # 999003 Jo

# Re-point the bootstrap's "current" gameweek at GW10 instead of GW1.
BOOTSTRAP = copy.deepcopy(BOOTSTRAP)
for ev in BOOTSTRAP["events"]:
    ev["finished"] = ev["id"] < GW
    ev["is_previous"] = ev["id"] < GW
    ev["is_current"] = ev["id"] == GW
    ev["is_next"] = ev["id"] == GW + 1

TRANSFERS_A = [
    {"event": 3, "element_in": 401, "element_in_cost": 100, "element_out": 30, "element_out_cost": 55},
    {"event": 9, "element_in": 155, "element_in_cost": 65, "element_out": 4, "element_out_cost": 60},
    {"event": 10, "element_in": 379, "element_in_cost": 90, "element_out": 31, "element_out_cost": 45},
]
# All old — none should survive the event >= GW-2=8 filter.
TRANSFERS_C = [
    {"event": 2, "element_in": 3, "element_in_cost": 45, "element_out": 1, "element_out_cost": 50},
    {"event": 5, "element_in": 7, "element_in_cost": 50, "element_out": 9, "element_out_cost": 45},
]


async def main():
    from app import fpl_client

    async def _fetch(path, ttl=None):
        if path == "/bootstrap-static/":
            return BOOTSTRAP
        if path == "/fixtures/":
            return FIXTURES
        if path == f"/leagues-classic/{LEAGUE_ID}/standings/":
            return STANDINGS
        if path == f"/entry/999001/event/{GW}/picks/":
            return PICKS_A
        if path == f"/entry/999002/event/{GW}/picks/":
            return PICKS_B
        if path == f"/entry/999003/event/{GW}/picks/":
            return PICKS_C
        if path == f"/entry/999004/event/{GW}/picks/":
            raise RuntimeError("simulated fetch failure for Kim Failure")
        if path == "/entry/999001/transfers/":
            return TRANSFERS_A
        if path == "/entry/999003/transfers/":
            return TRANSFERS_C
        raise AssertionError(f"un-stubbed path {path!r}")

    fpl_client._fetch = _fetch
    fpl_client._cache.clear()

    from app.algorithms.rivals import get_rival_analysis

    result = await get_rival_analysis(LEAGUE_ID, USER_TEAM_ID)

    TESTDATA.mkdir(parents=True, exist_ok=True)
    (TESTDATA / "bootstrap.json").write_text(json.dumps(BOOTSTRAP))
    (TESTDATA / "fixtures.json").write_text(json.dumps(FIXTURES))
    (TESTDATA / "standings.json").write_text(json.dumps(STANDINGS))
    (TESTDATA / "picks_a.json").write_text(json.dumps(PICKS_A))
    (TESTDATA / "picks_b.json").write_text(json.dumps(PICKS_B))
    (TESTDATA / "picks_c.json").write_text(json.dumps(PICKS_C))
    (TESTDATA / "transfers_a.json").write_text(json.dumps(TRANSFERS_A))
    (TESTDATA / "transfers_c.json").write_text(json.dumps(TRANSFERS_C))
    (TESTDATA / "golden.json").write_text(json.dumps(result, indent=2, sort_keys=True))

    print(f"gameweek={result.get('gameweek')} standings_as_of={result.get('standings_as_of_gw')}")
    print(f"rivals analyzed: {len(result.get('rivals', []))}")
    for r in result.get("rivals", []):
        has_pred = "transfer_prediction" in r
        recent = r.get("recent_transfers")
        print(f"  {r['manager_name']}: gap={r['point_gap']} ({r['gap_direction']}) "
              f"has_prediction={has_pred} recent_transfers={recent}")
    print()
    for s in result.get("strategy", []):
        print(" -", s)
    print(f"\nwrote {TESTDATA}")


if __name__ == "__main__":
    asyncio.run(main())
