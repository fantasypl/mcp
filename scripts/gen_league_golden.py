"""
Golden fixture for league_analyzer.analyze_league — a hand-built 4-manager
mini-league, since this algorithm needs multiple managers' squads, histories,
and a standings table all fetched together, which doesn't fit the
single-squad pattern the other entry/-dependent cases share in gen_golden.py.

Scenario, deliberately spanning the interesting branches. Note the win
probability model is a composite of points gap, squad quality, chips, and
momentum — not raw standings order — so it does not necessarily crown the
actual points leader as favourite, and this fixture's outcome makes that
concrete rather than assuming it away:
  - Manager A "Title Chaser" — leads the standings and is on a hot run of
    form, but a lower squad_quality score than B leaves B as the computed
    favourite at GW1 (season_progress=0, so squad quality is weighted
    heavily against the still-small points gap). Exercises the hot-streak
    insight for a non-favourite.
  - Manager B "Close Rival" — 8pts behind Chaser on raw points, but the
    computed favourite. Exercises the "tight race" insight — note the gap is
    computed favourite-total minus next-total with no abs(), so it prints
    negative here (real reference behaviour, not a bug).
  - Manager C "Chip Hoarder" — a distinct squad from A/B, with two starters
    manually marked injured plus one (White, id 10) already injured in the
    frozen bootstrap — three total, exercising the top-3 injury insight — and
    3 of 4 chips unused, exercising the chip-advantage insight.
  - Manager D "Fetch Failure" — has no picks or history stubbed at all,
    forcing the per-manager error path (Could not fetch squad data) without
    aborting the other three managers' analysis.

Usage: cd reference/python-src && uv run python ../../scripts/gen_league_golden.py
"""

import asyncio
import copy
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
TESTDATA = ROOT / "testdata" / "league_scenario"
REFERENCE = ROOT / "reference" / "python-src"
sys.path.insert(0, str(REFERENCE))

LEAGUE_ID = 12345
GW = 1

BOOTSTRAP_SRC = json.loads((TESTDATA.parent / "bootstrap_midseason.json").read_text())
FIXTURES = json.loads((TESTDATA.parent / "fixtures.json").read_text())
SQUAD_A = json.loads((TESTDATA.parent / "picks_squad1.json").read_text())

# Two more distinct squads, built from real (non-overlapping) player IDs.
SQUAD_B_ELEMENTS = [384, 2, 6, 200, 229, 388, 389, 397, 427, 428, 480, 78, 106, 25, 26]
SQUAD_C_ELEMENTS = [1, 3, 7, 9, 10, 14, 15, 20, 21, 27, 28, 29, 32, 33, 34]
INJURED_IN_C = {SQUAD_C_ELEMENTS[0], SQUAD_C_ELEMENTS[1]}  # both starters (positions 1-2)


def make_picks(elements, captain_idx=0, vice_idx=1):
    picks = []
    for i, eid in enumerate(elements[:11], start=1):
        picks.append({
            "element": eid, "position": i,
            "multiplier": 2 if i - 1 == captain_idx else 1,
            "is_captain": i - 1 == captain_idx, "is_vice_captain": i - 1 == vice_idx,
        })
    for offset, eid in enumerate(elements[11:15]):
        picks.append({"element": eid, "position": 12 + offset, "multiplier": 0,
                       "is_captain": False, "is_vice_captain": False})
    return picks


def entry_history(bank):
    return {"bank": bank, "event_transfers": 1, "overall_rank": 50000,
            "total_points": 0, "points_on_bench": 0}


PICKS_B = {"picks": make_picks(SQUAD_B_ELEMENTS), "active_chip": None, "entry_history": entry_history(12)}
PICKS_C = {"picks": make_picks(SQUAD_C_ELEMENTS), "active_chip": None, "entry_history": entry_history(3)}

STANDINGS = {
    "league": {"id": LEAGUE_ID, "name": "The Bootleg Bin"},
    "standings": {"results": [
        {"entry": 999001, "entry_name": "Title Chaser", "player_name": "Alex Chaser",
         "rank": 1, "total": 1850, "event_total": 68},
        {"entry": 999002, "entry_name": "Close Rival", "player_name": "Sam Rival",
         "rank": 2, "total": 1842, "event_total": 55},
        {"entry": 999003, "entry_name": "Chip Hoarder", "player_name": "Jo Hoarder",
         "rank": 3, "total": 1690, "event_total": 40},
        {"entry": 999004, "entry_name": "Fetch Failure", "player_name": "Kim Failure",
         "rank": 4, "total": 1600, "event_total": 35},
    ]},
}

# history.current: last-5-GW points feed momentum. A: hot (avg ~74.6). B/C middling.
HISTORY_A = {"current": [{"event": g, "points": p} for g, p in
                          [(GW - 4, 78), (GW - 3, 70), (GW - 2, 82), (GW - 1, 75), (GW, 68)] if g >= 1],
             "chips": [{"name": "wildcard", "event": 3}]}
HISTORY_B = {"current": [{"event": g, "points": p} for g, p in
                          [(GW - 4, 55), (GW - 3, 60), (GW - 2, 58), (GW - 1, 62), (GW, 55)] if g >= 1],
             "chips": [{"name": "wildcard", "event": 2}, {"name": "bboost", "event": 4}]}
HISTORY_C = {"current": [{"event": g, "points": p} for g, p in
                          [(GW - 4, 40), (GW - 3, 45), (GW - 2, 38), (GW - 1, 42), (GW, 40)] if g >= 1],
             "chips": []}  # 3 of 4 chips remaining

BOOTSTRAP = copy.deepcopy(BOOTSTRAP_SRC)
for el in BOOTSTRAP["elements"]:
    if el["id"] in INJURED_IN_C:
        el["status"] = "i"
BOOTSTRAP["element_types"] = []
BOOTSTRAP["events"] = [
    {
        "id": gw, "finished": gw < GW, "is_previous": gw < GW,
        "is_current": gw == GW, "is_next": gw == GW + 1,
        "average_entry_score": 0, "highest_score": None,
        "top_element": None, "most_captained": None, "chip_plays": [],
    }
    for gw in range(1, 39)
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
            return SQUAD_A
        if path == f"/entry/999002/event/{GW}/picks/":
            return PICKS_B
        if path == f"/entry/999003/event/{GW}/picks/":
            return PICKS_C
        if path == "/entry/999001/history/":
            return HISTORY_A
        if path == "/entry/999002/history/":
            return HISTORY_B
        if path == "/entry/999003/history/":
            return HISTORY_C
        if path in (f"/entry/999004/event/{GW}/picks/", "/entry/999004/history/"):
            raise RuntimeError("simulated fetch failure for manager D")
        raise AssertionError(f"un-stubbed path {path!r}")

    fpl_client._fetch = _fetch
    fpl_client._cache.clear()

    from app.algorithms.league_analyzer import analyze_league

    result = await analyze_league(LEAGUE_ID)

    TESTDATA.mkdir(parents=True, exist_ok=True)
    (TESTDATA / "bootstrap.json").write_text(json.dumps(BOOTSTRAP))
    (TESTDATA / "fixtures.json").write_text(json.dumps(FIXTURES))
    (TESTDATA / "standings.json").write_text(json.dumps(STANDINGS))
    (TESTDATA / "picks_a.json").write_text(json.dumps(SQUAD_A))
    (TESTDATA / "picks_b.json").write_text(json.dumps(PICKS_B))
    (TESTDATA / "picks_c.json").write_text(json.dumps(PICKS_C))
    (TESTDATA / "history_a.json").write_text(json.dumps(HISTORY_A))
    (TESTDATA / "history_b.json").write_text(json.dumps(HISTORY_B))
    (TESTDATA / "history_c.json").write_text(json.dumps(HISTORY_C))
    (TESTDATA / "golden.json").write_text(json.dumps(result, indent=2, sort_keys=True))

    for m in result["managers"]:
        if "error" in m:
            print(f"{m['manager_name']}: ERROR - {m['error']}")
        else:
            print(f"{m['manager_name']}: win_prob={m['win_probability']}% squad_quality={m['squad_quality']} "
                  f"chips={m['chips_remaining']} injured={m['injured_starters']}")
    print()
    for i in result["insights"]:
        print(" -", i)
    print(f"\nwrote {TESTDATA}")


if __name__ == "__main__":
    asyncio.run(main())
