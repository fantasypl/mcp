"""
Golden fixture for chips.get_chip_strategy — a hand-built 10-gameweek scan
window with a confirmed DGW, a confirmed BGW, and a postponed fixture, plus
a stubbed community-intel fetch (the real one hits premierleague.com and
allaboutfpl.com over HTTP, which a golden-file generator must never do).

Squad reuses testdata/picks_squad1.json (real player IDs spanning ARS, AVL,
BOU, BRE, BHA, CHE, EVE, LIV, MCI, MUN). History has Triple Captain already
used, leaving Wildcard, Bench Boost and Free Hit remaining — three chips,
enough to force the combinatorial (not single-chip) search path and exercise
the Wildcard->Bench Boost combo bonus specifically.

Scenario, GW1-10 scan window:
  - GW1-9: every team plays exactly once (20 teams, 10 fixtures/GW), except
    where overridden below.
  - GW5: Man City (team 15, in the squad) gets a genuine double gameweek —
    the mega-DGW Bench Boost should target this.
  - GW6: two teams (19, 20 — not in the squad) blank entirely, a BGW signal
    for Free Hit scoring.
  - One postponed fixture (Arsenal vs Spurs, event=null, not finished) feeds
    predict_dgw_teams / likely_dgw_gws — the "future DGW we don't know the
    date of yet" signal, independent of GW5's already-confirmed one.
  - Community intel (stubbed, not fetched over HTTP) adds a predicted BGW for
    GW7 covering a team already blanking via the API in that GW plus one
    more, and a predicted DGW for GW8 — exercising the merge path without
    ever making a real request.

Usage: cd reference/python-src && uv run python ../../scripts/gen_chips_golden.py
"""

import asyncio
import copy
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
TESTDATA = ROOT / "testdata" / "chips_scenario"
REFERENCE = ROOT / "reference" / "python-src"
sys.path.insert(0, str(REFERENCE))

TEAM_ID = 999001
GW = 1

BOOTSTRAP_SRC = json.loads((TESTDATA.parent / "bootstrap_midseason.json").read_text())
SQUAD = json.loads((TESTDATA.parent / "picks_squad1.json").read_text())
TEAMS = BOOTSTRAP_SRC["teams"]
TEAM_IDS = sorted(t["id"] for t in TEAMS)  # 1..20

BOOTSTRAP = copy.deepcopy(BOOTSTRAP_SRC)
BOOTSTRAP["element_types"] = []
BOOTSTRAP["events"] = [
    {
        "id": gw, "finished": gw < GW, "is_previous": gw < GW,
        "is_current": gw == GW, "is_next": gw == GW + 1,
        "average_entry_score": 0, "highest_score": None,
        "top_element": None, "most_captained": None,
        "chip_plays": [{"chip_name": "3xc", "num_played": 12345}] if gw == GW else [],
    }
    for gw in range(1, 39)
]

HISTORY = {"current": [], "chips": [{"name": "3xc", "event": 1}]}


def round_robin_fixtures(gw, start_fid, skip_teams=None, extra_pairs=None):
    """One fixture per team for a gameweek, pairing team i with team i+10,
    shifted by gw so match-ups differ week to week — deterministic, no
    particular football meaning, just distinct pairings per gameweek."""
    skip_teams = skip_teams or set()
    ids = [t for t in TEAM_IDS if t not in skip_teams]
    fixtures = []
    fid = start_fid
    half = len(ids) // 2
    shift = gw % half
    home = ids[:half]
    away = ids[half:][shift:] + ids[half:][:shift]
    for h, a in zip(home, away):
        fixtures.append({
            "id": fid, "event": gw, "team_h": h, "team_a": a,
            "team_h_difficulty": 3, "team_a_difficulty": 3,
            "finished": gw < GW, "started": gw < GW, "finished_provisional": gw < GW,
            "minutes": 90 if gw < GW else 0,
            "team_h_score": 1 if gw < GW else None, "team_a_score": 1 if gw < GW else None,
        })
        fid += 1
    if extra_pairs:
        for h, a in extra_pairs:
            fixtures.append({
                "id": fid, "event": gw, "team_h": h, "team_a": a,
                "team_h_difficulty": 2, "team_a_difficulty": 4,
                "finished": False, "started": False, "finished_provisional": False,
                "minutes": 0, "team_h_score": None, "team_a_score": None,
            })
            fid += 1
    return fixtures, fid


FIXTURES = []
fid = 1
for gw in range(1, 11):
    if gw == 5:
        # Man City (15) double gameweek: a normal fixture plus one extra.
        fx, fid = round_robin_fixtures(gw, fid, extra_pairs=[(15, 8)])
    elif gw == 6:
        # Teams 19 and 20 blank entirely this gameweek.
        fx, fid = round_robin_fixtures(gw, fid, skip_teams={19, 20})
    else:
        fx, fid = round_robin_fixtures(gw, fid)
    FIXTURES.extend(fx)

# A postponed fixture: Arsenal (1) vs Spurs (17), no gameweek assigned yet.
FIXTURES.append({
    "id": fid, "event": None, "team_h": 1, "team_a": 17,
    "team_h_difficulty": 3, "team_a_difficulty": 3,
    "finished": False, "started": False, "finished_provisional": False,
    "minutes": 0, "team_h_score": None, "team_a_score": None,
})

# Remaining gameweeks (11-38) so bootstrap.events math and any incidental
# lookups outside the scan window don't hit missing fixtures.
for gw in range(11, 39):
    fx, fid = round_robin_fixtures(gw, fid)
    FIXTURES.extend(fx)


# Stubbed community intel — never fetched over HTTP. GW7's predicted BGW adds
# team 18 (already-playing, per the round robin) as an *additional* blank
# beyond the API-confirmed set, and GW8 gets a predicted DGW for team 2.
def team_short(tid):
    return next(t["short_name"] for t in TEAMS if t["id"] == tid)


COMMUNITY_INTEL = {
    "dgws": {"8": {"teams": [team_short(2)], "status": "predicted", "sources": ["allaboutfpl.com"]}},
    "bgws": {"7": {"teams": [team_short(18)], "status": "predicted", "sources": ["allaboutfpl.com"]}},
    "sources_checked": ["premierleague.com", "allaboutfpl.com"],
    "errors": ["premierleague.com: failed to fetch"],
}


async def main():
    from app import fpl_client

    async def _fetch(path, ttl=None):
        if path == "/bootstrap-static/":
            return BOOTSTRAP
        if path == "/fixtures/":
            return FIXTURES
        if path == f"/entry/{TEAM_ID}/event/{GW}/picks/":
            return SQUAD
        if path == f"/entry/{TEAM_ID}/history/":
            return HISTORY
        raise AssertionError(f"un-stubbed path {path!r}")

    fpl_client._fetch = _fetch
    fpl_client._cache.clear()

    import app.algorithms.chips as chips_module

    async def _fake_intel():
        return COMMUNITY_INTEL

    chips_module.fetch_community_dgw_intel = _fake_intel

    result = await chips_module.get_chip_strategy(TEAM_ID)

    TESTDATA.mkdir(parents=True, exist_ok=True)
    (TESTDATA / "bootstrap.json").write_text(json.dumps(BOOTSTRAP))
    (TESTDATA / "fixtures.json").write_text(json.dumps(FIXTURES))
    (TESTDATA / "picks.json").write_text(json.dumps(SQUAD))
    (TESTDATA / "history.json").write_text(json.dumps(HISTORY))
    (TESTDATA / "community_intel.json").write_text(json.dumps(COMMUNITY_INTEL))
    (TESTDATA / "golden.json").write_text(json.dumps(result, indent=2, sort_keys=True))

    print(f"chips_remaining={result.get('chips_remaining')}")
    print(f"scan_window={result.get('scan_window')}")
    for r in result.get("recommendations", []):
        print(f"  {r['chip']}: GW{r['recommended_gameweek']} (score {r['confidence_score']})")
        print(f"    {r['reasoning']}")
    if "pending_dgws" in result:
        print("pending_dgws:", result["pending_dgws"]["summary"])
    if "community_intel" in result:
        print("community_intel keys:", list(result["community_intel"].keys()))
    print(f"\nwrote {TESTDATA}")


if __name__ == "__main__":
    asyncio.run(main())
