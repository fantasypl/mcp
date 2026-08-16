"""
Golden fixture for live.get_live_points — a hand-built, fully-controlled
gameweek scenario, unlike the parametrized cases in gen_golden.py.

live_points needs five concurrently-fetched inputs (picks, live scores,
history, event-status, fixtures) and its output depends on match state
(not_started / live / finished), BPS ties, and auto-sub triggers that don't
occur naturally in a frozen real payload — the real GW1 fixtures haven't been
played yet, so there's no live data to exercise any of this against. This
builds a minimal, deliberately spiky scenario instead:

  - ARS 3-1 MUN, finished: Gabriel and Rice tied at the top BPS (both get the
    3-bonus, consuming both top slots per FPL's tie rule) so J.Timber drops
    straight to the 1-bonus slot, skipping 2. Confirmed bonus is set for the
    two leaders, projected for the rest — exercises both bonus_status paths
    in one fixture.
  - AVL vs CHE, live (started, not finished): no ties, projected bonus only.
    A.Garcia is an unused sub (0 minutes) despite being in the squad's
    starting XI.
  - BHA 2-0 BRE, finished: gives the bench (Howell, Furo) real minutes so
    they're eligible for auto-subs.
  - MCI vs LIV, not started: Marmoush and Isak both read as 0 minutes,
    exactly like an unused starter would — the algorithm can't tell "hasn't
    kicked off yet" from "an actual squad exclusion" and shouldn't need to.
  - BOU 1-0 NEW, finished: gives bench GKP Forster real minutes.

Net effect: Pickford (GKP) and three outfield starters (Marmoush, Isak,
A.Garcia) all read 0 minutes, forcing 4 auto-sub suggestions that have to
consume the bench in strict position order (12, 13, 14, 15) without reusing a
bench player twice — the scenario this file exists to prove correct.

Usage: cd reference/python-src && uv run python ../../scripts/gen_live_golden.py
"""

import asyncio
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
TESTDATA = ROOT / "testdata" / "live_scenario"
REFERENCE = ROOT / "reference" / "python-src"
sys.path.insert(0, str(REFERENCE))

TEAM_ID = 999001
GW = 1

# Real player IDs and teams, taken from testdata/picks_squad1.json against
# the midseason bootstrap, so this scenario stays consistent with the squad
# fixture the other entry/-dependent algorithms already use.
SQUAD = json.loads((TESTDATA.parent / "picks_squad1.json").read_text())
BOOTSTRAP_SRC = json.loads((TESTDATA.parent / "bootstrap_midseason.json").read_text())
PLAYERS = {e["id"]: e for e in BOOTSTRAP_SRC["elements"]}
TEAMS = {t["id"]: t for t in BOOTSTRAP_SRC["teams"]}

# element -> (minutes, bps, confirmed_bonus)
LIVE_STATS = {
    426: (90, 15, 0),   # B.Fernandes (MUN) — lowest BPS in a finished match, no bonus
    4: (90, 35, 3),     # Gabriel (ARS) — tied top BPS, confirmed
    13: (90, 35, 3),    # Rice (ARS) — tied top BPS, confirmed
    5: (90, 20, 1),     # J.Timber (ARS) — drops to the 1-bonus slot (ties consumed 3,2)
    155: (90, 45, 0),   # Enzo (CHE) — live match, rank 1, projected only
    31: (90, 30, 0),    # Konsa (AVL) — live match, rank 2, projected
    30: (90, 10, 0),    # Digne (AVL) — live match, rank 3, projected
    38: (0, 0, 0),      # A.Garcia (AVL) — unused sub despite starting
    132: (90, 25, 2),   # Howell (BHA, bench) — played, confirmed bonus
    134: (0, 0, 0),     # Oriola (BHA, bench) — did not play
    107: (90, 10, 0),   # Furo (BRE, bench) — played
    58: (90, 12, 0),    # Forster (BOU, bench GKP) — played
    # Pickford (226, GKP starter), Marmoush (401), Isak (379) intentionally
    # absent from live data entirely — a not-yet-started fixture (Marmoush,
    # Isak) or an unused GKP sub (Pickford) both surface identically: no
    # entry, which the algorithm must treat as 0 minutes, not an error.
}

FIXTURES = [
    {"id": 1, "event": GW, "team_h": 1, "team_a": 16, "team_h_difficulty": 2,
     "team_a_difficulty": 3, "started": True, "finished": True,
     "finished_provisional": True, "minutes": 90, "team_h_score": 3, "team_a_score": 1},
    {"id": 2, "event": GW, "team_h": 2, "team_a": 6, "team_h_difficulty": 3,
     "team_a_difficulty": 3, "started": True, "finished": False,
     "finished_provisional": False, "minutes": 67, "team_h_score": 1, "team_a_score": 1},
    {"id": 3, "event": GW, "team_h": 5, "team_a": 4, "team_h_difficulty": 3,
     "team_a_difficulty": 3, "started": True, "finished": True,
     "finished_provisional": True, "minutes": 90, "team_h_score": 2, "team_a_score": 0},
    {"id": 4, "event": GW, "team_h": 15, "team_a": 14, "team_h_difficulty": 4,
     "team_a_difficulty": 2, "started": False, "finished": False,
     "finished_provisional": False, "minutes": 0, "team_h_score": None, "team_a_score": None},
    {"id": 5, "event": GW, "team_h": 3, "team_a": 10, "team_h_difficulty": 3,
     "team_a_difficulty": 3, "started": True, "finished": True,
     "finished_provisional": True, "minutes": 90, "team_h_score": 1, "team_a_score": 0},
]

TOP_ELEMENT = 4  # Gabriel — the confirmed joint-top scorer, used for bootstrap.top_element

BOOTSTRAP = {
    "elements": BOOTSTRAP_SRC["elements"],
    "teams": BOOTSTRAP_SRC["teams"],
    "element_types": [],
    "events": [
        {
            "id": gw,
            "finished": gw < GW,
            "is_previous": gw < GW,
            "is_current": gw == GW,
            "is_next": gw == GW + 1,
            "average_entry_score": 52 if gw == GW else 0,
            "highest_score": 98 if gw == GW else None,
            "top_element": TOP_ELEMENT if gw == GW else None,
            "most_captained": None,
            "chip_plays": [],
        }
        for gw in range(1, 39)
    ],
}

LIVE = {
    "elements": [
        {"id": eid, "stats": {"minutes": m, "total_points": 0, "bps": b, "bonus": c}}
        for eid, (m, b, c) in LIVE_STATS.items()
    ]
}
# total_points needs a value independent of bonus for a believable scenario:
# derive a small positive score from bps so the live_total is nonzero without
# hand-picking every figure.
for el in LIVE["elements"]:
    el["stats"]["total_points"] = max(0, el["stats"]["bps"] // 5) if el["stats"]["minutes"] > 0 else 0

EVENT_STATUS = {
    "status": [
        {"date": "2026-03-14", "bonus_added": True},
        {"date": "2026-03-15", "bonus_added": False},
    ],
    "leagues": "",
}


async def main():
    from app import fpl_client

    async def _fetch(path, ttl=None):
        if path == "/bootstrap-static/":
            return BOOTSTRAP
        if path == "/fixtures/":
            return FIXTURES
        if path == f"/event/{GW}/live/":
            return LIVE
        if path == "/event-status/":
            return EVENT_STATUS
        if path == f"/entry/{TEAM_ID}/event/{GW}/picks/":
            return SQUAD
        if path == f"/entry/{TEAM_ID}/history/":
            return {"current": [], "past": [], "chips": []}
        raise AssertionError(f"un-stubbed path {path!r}")

    fpl_client._fetch = _fetch
    fpl_client._cache.clear()

    from app.algorithms.live import get_live_points

    result = await get_live_points(TEAM_ID)

    TESTDATA.mkdir(parents=True, exist_ok=True)
    (TESTDATA / "bootstrap.json").write_text(json.dumps(BOOTSTRAP))
    (TESTDATA / "fixtures.json").write_text(json.dumps(FIXTURES))
    (TESTDATA / "live.json").write_text(json.dumps(LIVE))
    (TESTDATA / "event_status.json").write_text(json.dumps(EVENT_STATUS))
    (TESTDATA / "picks.json").write_text(json.dumps(SQUAD))
    (TESTDATA / "golden.json").write_text(json.dumps(result, indent=2, sort_keys=True))

    print(f"live_total={result['live_total']} bonus_status={result['bonus_status']}")
    print(f"auto_sub_scenarios={len(result['auto_sub_scenarios'])}")
    for s in result["auto_sub_scenarios"]:
        print(f"  {s['out']} -> {s['in']} ({s['note']})")
    print(f"wrote {TESTDATA}")


if __name__ == "__main__":
    asyncio.run(main())
