"""
Reconstruct genuine point-in-time FPL state from vaastav/Fantasy-Premier-League
and use it to validate the captain-pick algorithm against a real historical
result — not a Python/Go parity check, but "does the model's #1 recommendation
actually tend to score."

Why reconstruction rather than replay: the reference project's own
scripts/backtest.py runs the algorithm against a single *current* bootstrap
snapshot for every past gameweek it backtests, which is look-ahead biased —
"predicting" GW10 using stats that already include GW11-38. That is not a fair
test of predictive skill.

vaastav's per-gameweek CSVs (data/{season}/gws/gw{N}.csv) are match-level
rows, not running totals, so summing gw1..gw{N-1} recovers exactly what a
manager would have seen going into gameweek N: season-to-date totals, no
future data. This script does that summation and emits a bootstrap+fixtures
JSON pair in the same shape the real FPL API returns — decodable by the
already-tested Go client/algorithm types with no special-casing.

Known gaps versus a live run, all on small-weight terms:
  - ep_next (FPL's own point prediction, weight 0.49) isn't published
    anywhere outside the live API and can't be reconstructed; left at 0.
  - Set-piece and penalty duty (penalties_order etc.) are roster metadata,
    not match output; left null, so nobody gets the set-piece/penalty bonus.
  - status/chance_of_playing_next_round can't be reconstructed either;
    everyone is treated as available. In practice this is self-correcting —
    a player who wasn't actually playing already has near-zero recent form.
  - Team strength ratings come from a single current teams.csv snapshot
    rather than a point-in-time one; strength moves slowly, so this is a
    minor approximation, and the reference's own backtest has the same
    property.
The dominant scoring terms — form, points-per-game, xG/90, xA/90, ICT,
minutes certainty, and the fixture multiplier — are all reconstructed from
real match data with no look-ahead.

Usage:
    python scripts/backtest_from_vaastav.py --season 2025-26 --predict-gw 38
    python scripts/backtest_from_vaastav.py --season 2025-26 --from-gw 28 --to-gw 38
"""

import argparse
import csv
import io
import json
import pathlib
import urllib.request
from collections import defaultdict

ROOT = pathlib.Path(__file__).resolve().parent.parent
CACHE = ROOT / ".cache" / "vaastav"
BASE = "https://raw.githubusercontent.com/vaastav/Fantasy-Premier-League/HEAD/data"

POSITION_TO_ELEMENT_TYPE = {"GKP": 1, "GK": 1, "DEF": 2, "MID": 3, "FWD": 4}

# Fields summed as running season totals.
SUM_FIELDS = [
    "total_points", "minutes", "starts", "goals_scored", "assists", "bonus",
    "bps", "clean_sheets", "yellow_cards", "red_cards", "expected_goals",
    "expected_assists", "expected_goal_involvements", "ict_index",
    "influence", "creativity", "threat",
]


def _fetch(path: str) -> str:
    cache_path = CACHE / path
    if cache_path.exists():
        return cache_path.read_text()
    url = f"{BASE}/{path}"
    with urllib.request.urlopen(url) as r:
        text = r.read().decode()
    cache_path.parent.mkdir(parents=True, exist_ok=True)
    cache_path.write_text(text)
    return text


def _csv_rows(path: str) -> list[dict]:
    return list(csv.DictReader(io.StringIO(_fetch(path))))


def num(s, default=0.0):
    if s is None or s == "":
        return default
    try:
        return float(s)
    except ValueError:
        return default


def load_teams(season: str):
    rows = _csv_rows(f"{season}/teams.csv")
    by_id, name_to_id = {}, {}
    for r in rows:
        tid = int(r["id"])
        by_id[tid] = r
        name_to_id[r["name"]] = tid
    return by_id, name_to_id


def load_fixtures(season: str):
    rows = _csv_rows(f"{season}/fixtures.csv")
    out = []
    for r in rows:
        if not r.get("event"):
            continue
        out.append(
            {
                "id": int(r["id"]),
                "event": int(r["event"]),
                "team_h": int(r["team_h"]),
                "team_a": int(r["team_a"]),
                "team_h_difficulty": int(r["team_h_difficulty"] or 3),
                "team_a_difficulty": int(r["team_a_difficulty"] or 3),
                "finished": r.get("finished") == "True",
                "started": r.get("started") == "True",
                "kickoff_time": r.get("kickoff_time") or "",
                "minutes": int(r["minutes"] or 0),
            }
        )
    return out


def build_state(season: str, upto_gw: int, name_to_id: dict):
    """Sum gws/gw1.csv..gw{upto_gw}.csv into season-to-date player state."""
    totals: dict[int, dict] = {}
    meta: dict[int, dict] = {}

    for gw in range(1, upto_gw + 1):
        for row in _csv_rows(f"{season}/gws/gw{gw}.csv"):
            eid = int(row["element"])
            if eid not in totals:
                totals[eid] = defaultdict(float)
                meta[eid] = {
                    "name": row["name"],
                    "position": row["position"],
                    "team_name": row["team"],
                }
            for f in SUM_FIELDS:
                totals[eid][f] += num(row.get(f))
            meta[eid]["now_cost"] = int(num(row.get("value"), 0))  # latest value wins
            meta[eid]["team_name"] = row["team"]

    # Form: FPL's own definition is roughly the last 30 days / ~5 GWs. Recompute
    # from the most recent window rather than the running sum above, since form
    # is explicitly NOT a season-to-date figure.
    recent_points = defaultdict(list)
    window_start = max(1, upto_gw - 4)
    for gw in range(window_start, upto_gw + 1):
        seen_this_gw = set()
        for row in _csv_rows(f"{season}/gws/gw{gw}.csv"):
            eid = int(row["element"])
            recent_points[eid].append(num(row.get("total_points")))
            seen_this_gw.add(eid)

    players = []
    for eid, t in totals.items():
        m = meta[eid]
        games_played = sum(
            1
            for gw in range(1, upto_gw + 1)
            if _appeared(season, gw, eid)
        )
        form_window = recent_points.get(eid, [])
        form = sum(form_window) / len(form_window) if form_window else 0.0
        ppg = t["total_points"] / games_played if games_played else 0.0

        players.append(
            {
                "id": eid,
                "code": eid,
                "team": name_to_id.get(m["team_name"], 0),
                "element_type": POSITION_TO_ELEMENT_TYPE.get(m["position"], 3),
                "web_name": m["name"],
                "status": "a",
                "news": "",
                "news_added": None,
                "chance_of_playing_next_round": None,
                "total_points": int(t["total_points"]),
                "form": round(form, 1),
                "points_per_game": round(ppg, 1),
                "ep_next": 0.0,  # not reconstructable — see module docstring
                "now_cost": m["now_cost"],
                "selected_by_percent": 0.0,  # not scored; display-only field
                "minutes": int(t["minutes"]),
                "starts": int(t["starts"]),
                "bonus": int(t["bonus"]),
                "bps": int(t["bps"]),
                "clean_sheets": int(t["clean_sheets"]),
                "yellow_cards": int(t["yellow_cards"]),
                "red_cards": int(t["red_cards"]),
                "dreamteam_count": 0,
                "ict_index": round(t["ict_index"], 1),
                "influence": round(t["influence"], 1),
                "creativity": round(t["creativity"], 1),
                "threat": round(t["threat"], 1),
                "expected_goals": round(t["expected_goals"], 2),
                "expected_assists": round(t["expected_assists"], 2),
                "expected_goal_involvements": round(t["expected_goal_involvements"], 2),
                "defensive_contribution_per_90": 0.0,  # per-90 not in per-gw export
                "penalties_order": None,
                "corners_and_indirect_freekicks_order": None,
                "direct_freekicks_order": None,
                "scout_risks": [],
            }
        )

    return players


_APPEARANCE_CACHE: dict = {}


def _appeared(season, gw, element_id) -> bool:
    key = (season, gw)
    if key not in _APPEARANCE_CACHE:
        rows = _csv_rows(f"{season}/gws/gw{gw}.csv")
        _APPEARANCE_CACHE[key] = {int(r["element"]) for r in rows if num(r.get("minutes")) > 0}
    return element_id in _APPEARANCE_CACHE[key]


def build_teams_json(teams_by_id: dict):
    out = []
    for tid, r in teams_by_id.items():
        out.append(
            {
                "id": tid,
                "code": int(r.get("code") or 0),
                "name": r["name"],
                "short_name": r["short_name"],
                "strength": int(r["strength"]) if r.get("strength") else None,
                "position": int(r.get("position") or 0),
                "played": int(r.get("played") or 0),
                "points": int(r.get("points") or 0),
                "strength_overall_home": int(r.get("strength_overall_home") or 1200),
                "strength_overall_away": int(r.get("strength_overall_away") or 1200),
                "strength_attack_home": int(r.get("strength_attack_home") or 1200),
                "strength_attack_away": int(r.get("strength_attack_away") or 1200),
                "strength_defence_home": int(r.get("strength_defence_home") or 1200),
                "strength_defence_away": int(r.get("strength_defence_away") or 1200),
            }
        )
    return out


def actual_results(season: str, gw: int) -> dict:
    """element_id -> points actually scored in gw (not cumulative)."""
    out = {}
    for row in _csv_rows(f"{season}/gws/gw{gw}.csv"):
        out[int(row["element"])] = {
            "web_name": row["name"],
            "team": row["team"],
            "position": row["position"],
            "points": int(num(row.get("total_points"))),
            "minutes": int(num(row.get("minutes"))),
        }
    return out


def build_case(season: str, predict_gw: int, out_dir: pathlib.Path):
    upto_gw = predict_gw - 1
    print(f"[{season}] reconstructing state through GW{upto_gw} to predict GW{predict_gw}...")

    teams_by_id, name_to_id = load_teams(season)
    players = build_state(season, upto_gw, name_to_id)
    fixtures = load_fixtures(season)

    bootstrap = {
        "elements": players,
        "teams": build_teams_json(teams_by_id),
        "events": [],
        "element_types": [],
    }

    out_dir.mkdir(parents=True, exist_ok=True)
    (out_dir / f"bootstrap_gw{upto_gw}.json").write_text(json.dumps(bootstrap))
    (out_dir / "fixtures.json").write_text(json.dumps(fixtures))
    (out_dir / f"actual_gw{predict_gw}.json").write_text(
        json.dumps(actual_results(season, predict_gw), indent=1)
    )
    print(f"  wrote {out_dir}/bootstrap_gw{upto_gw}.json ({len(players)} players)")
    print(f"  wrote {out_dir}/fixtures.json ({len(fixtures)} fixtures)")
    print(f"  wrote {out_dir}/actual_gw{predict_gw}.json")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--season", default="2025-26")
    ap.add_argument("--predict-gw", type=int)
    ap.add_argument("--from-gw", type=int)
    ap.add_argument("--to-gw", type=int)
    ap.add_argument("--out", default=str(ROOT / "testdata" / "backtest"))
    args = ap.parse_args()

    out_root = pathlib.Path(args.out)

    if args.predict_gw:
        build_case(args.season, args.predict_gw, out_root / f"gw{args.predict_gw}")
    elif args.from_gw and args.to_gw:
        for gw in range(args.from_gw, args.to_gw + 1):
            build_case(args.season, gw, out_root / f"gw{gw}")
    else:
        raise SystemExit("pass --predict-gw N, or --from-gw N --to-gw M")


if __name__ == "__main__":
    main()
