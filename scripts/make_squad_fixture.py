"""
Build a synthetic, schema-valid squad fixture for testing the six algorithms
that need a manager's picks: transfers, live, chips, scout, rivals,
league_analyzer.

Why synthetic: verified live against the real FPL API while porting this
project — GET /entry/{id}/event/{gw}/picks/ has no season parameter, gameweek
numbers 1-38 are reused every season, and once a season rolls over its picks
are gone from every source, official or third-party, permanently. A specific
manager's historical squad selections cannot be sourced from anywhere. See the
plan's "entry/ data problem" note.

What makes this fixture usable anyway: the algorithms that consume picks only
care that the response is a *structurally valid* FPL picks payload —
real player IDs, correct budget, correct formation, correct is_captain /
multiplier semantics — joined against whatever bootstrap they fetch
separately. They cannot tell a synthetic squad from a real one. So this picks
15 real players from the frozen bootstrap, seeded for reproducibility, valid
under FPL's own squad rules:
  - exactly 2 GKP / 5 DEF / 5 MID / 3 FWD
  - total cost <= 100.0m
  - max 3 players from any one club
  - a valid starting XI formation (1 GKP, 3-5 DEF, 2-5 MID, 1-3 FWD, 11 total)

Usage: python scripts/make_squad_fixture.py
"""

import json
import pathlib
import random

ROOT = pathlib.Path(__file__).resolve().parent.parent
TESTDATA = ROOT / "testdata"

BUDGET_TENTHS = 1000  # £100.0m, in FPL's tenths-of-a-million unit
SQUAD_QUOTA = {1: 2, 2: 5, 3: 5, 4: 3}  # element_type -> count
MAX_PER_CLUB = 3


def load_bootstrap(name):
    return json.loads((TESTDATA / f"{name}.json").read_text())


def pick_squad(elements, seed):
    """Assemble a valid 15-man squad that actually spends close to the
    budget, the way a real manager's squad does — a cheapest-first fill
    passes every FPL constraint but produces an unrealistically bench-tier
    squad throughout, which under-exercises budget-constrained replacement
    logic (transfers.go filters candidates by "cost <= sell price + bank").

    Approach: for each slot, pick the player closest to a per-slot cost
    target (with a little random spread) rather than the cheapest available,
    subject to the running budget and the club-of-3 cap. Deterministic for a
    given seed.
    """
    rng = random.Random(seed)
    by_position = {pt: [] for pt in SQUAD_QUOTA}
    for e in elements:
        if e.get("status") in ("i", "u"):  # skip injured/unavailable for a clean fixture
            continue
        by_position.setdefault(e["element_type"], []).append(e)
    for pool in by_position.values():
        pool.sort(key=lambda e: e["now_cost"])

    # A realistic squad spends ~92-97% of budget. Spread targets so each
    # position has one pricier "premium" pick and several economy picks,
    # rather than a flat average per slot.
    target_spend = int(BUDGET_TENTHS * 0.95)
    slot_weight = {1: [0.55, 0.45], 2: [0.9, 0.7, 0.55, 0.45, 0.4],
                   3: [1.3, 0.9, 0.7, 0.5, 0.4], 4: [1.2, 0.8, 0.5]}
    total_weight = sum(sum(w) for w in slot_weight.values())
    targets = {
        pos: [target_spend * w / total_weight for w in weights]
        for pos, weights in slot_weight.items()
    }

    squad = []
    club_counts = {}
    spend = 0

    for pos, quota in SQUAD_QUOTA.items():
        pool = list(by_position[pos])
        for slot_idx in range(quota):
            target = targets[pos][slot_idx] * rng.uniform(0.85, 1.15)
            pool.sort(key=lambda e: abs(e["now_cost"] - target))
            for e in pool:
                if club_counts.get(e["team"], 0) >= MAX_PER_CLUB:
                    continue
                # Leave enough budget for every slot still to be filled,
                # assuming the cheapest remaining option at worst.
                remaining_after = sum(SQUAD_QUOTA.values()) - len(squad) - 1
                if spend + e["now_cost"] + remaining_after * 40 > BUDGET_TENTHS:
                    continue
                squad.append(e)
                pool.remove(e)
                club_counts[e["team"]] = club_counts.get(e["team"], 0) + 1
                spend += e["now_cost"]
                break
            else:
                raise SystemExit(f"could not fill position {pos} slot {slot_idx}")

    assert spend <= BUDGET_TENTHS, f"squad costs {spend/10}m, over budget"
    assert len(squad) == 15
    for pos, quota in SQUAD_QUOTA.items():
        assert sum(1 for e in squad if e["element_type"] == pos) == quota
    for team, count in club_counts.items():
        assert count <= MAX_PER_CLUB, f"team {team} has {count} players"

    return squad, spend


def choose_starting_xi(squad):
    """A valid formation: 1 GKP, plus enough DEF/MID/FWD to reach 11, subject
    to FPL's minimums (at least 3 DEF, 2 MID, 1 FWD)."""
    by_pos = {pt: [e for e in squad if e["element_type"] == pt] for pt in SQUAD_QUOTA}

    starters = [by_pos[1][0]]  # exactly one starting GKP
    bench_gkp = by_pos[1][1]

    outfield = by_pos[2] + by_pos[3] + by_pos[4]
    # Sort outfield by total_points desc so the "starting XI" at least looks
    # like a deliberate selection rather than arbitrary.
    outfield.sort(key=lambda e: -e.get("total_points", 0))

    def count(pos):
        return sum(1 for e in starters if e["element_type"] == pos)

    for e in outfield:
        if len(starters) >= 11:
            break
        pos = e["element_type"]
        # Respect minimums by not over-filling any one line before the others
        # have a chance to reach their floor.
        remaining_slots = 11 - len(starters)
        mins_still_needed = max(0, 3 - count(2)) + max(0, 2 - count(3)) + max(0, 1 - count(4))
        if remaining_slots <= mins_still_needed and (
            (pos == 2 and count(2) >= 3 and mins_still_needed <= remaining_slots)
            or (pos == 3 and count(3) >= 2)
            or (pos == 4 and count(4) >= 1)
        ):
            # Only take a line past its minimum once the other minimums are covered.
            if mins_still_needed >= remaining_slots:
                continue
        starters.append(e)

    bench_outfield = [e for e in squad if e not in starters and e["element_type"] != 1]
    bench = [bench_gkp] + bench_outfield

    assert len(starters) == 11, f"formation has {len(starters)} starters"
    assert len(bench) == 4
    def_n = sum(1 for e in starters if e["element_type"] == 2)
    mid_n = sum(1 for e in starters if e["element_type"] == 3)
    fwd_n = sum(1 for e in starters if e["element_type"] == 4)
    assert 3 <= def_n <= 5 and 2 <= mid_n <= 5 and 1 <= fwd_n <= 3, (def_n, mid_n, fwd_n)

    return starters, bench


def build_picks(squad, starters, bench, captain_id, vice_id):
    picks = []
    for pos, e in enumerate(starters, start=1):
        multiplier = 2 if e["id"] == captain_id else 1
        picks.append(
            {
                "element": e["id"],
                "position": pos,
                "multiplier": multiplier,
                "is_captain": e["id"] == captain_id,
                "is_vice_captain": e["id"] == vice_id,
            }
        )
    for offset, e in enumerate(bench):
        picks.append(
            {
                "element": e["id"],
                "position": 12 + offset,
                "multiplier": 0,
                "is_captain": False,
                "is_vice_captain": False,
            }
        )
    return picks


def main():
    bootstrap = load_bootstrap("bootstrap_midseason")  # has real form, so scoring differentiates
    elements = bootstrap["elements"]

    squad, spend = pick_squad(elements, seed=20260816)
    starters, bench = choose_starting_xi(squad)

    # Captain/vice: highest two total_points among starters, a plausible
    # real-manager choice rather than an arbitrary one.
    by_points = sorted(starters, key=lambda e: -e.get("total_points", 0))
    captain_id, vice_id = by_points[0]["id"], by_points[1]["id"]

    picks = build_picks(squad, starters, bench, captain_id, vice_id)

    fixture = {
        "picks": picks,
        "active_chip": None,
        "entry_history": {
            "bank": 5,  # £0.5m
            "event_transfers": 1,
            "overall_rank": 1_234_567,
            "total_points": sum(e.get("total_points", 0) for e in squad),
            "points_on_bench": 0,
        },
    }

    out = TESTDATA / "picks_squad1.json"
    out.write_text(json.dumps(fixture, indent=2, sort_keys=True))

    names = {e["id"]: e["web_name"] for e in squad}
    print(f"squad cost: {spend/10:.1f}m / 100.0m")
    print(f"captain: {names[captain_id]}, vice: {names[vice_id]}")
    print(f"starters: {[names[e['id']] for e in starters]}")
    print(f"bench: {[names[e['id']] for e in bench]}")
    print(f"wrote {out}")


if __name__ == "__main__":
    main()
