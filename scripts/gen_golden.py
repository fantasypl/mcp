"""
Generate golden outputs from the *Python* implementation.

These files are the parity contract for the Go port: Go must reproduce them
within 1e-6 for every algorithm. Run this once against the reference checkout,
commit the output, and never regenerate it casually — a regenerated golden file
that silently absorbs a Go bug defeats the entire harness.

The FPL client is stubbed to serve frozen payloads from testdata/, so this is
fully offline and deterministic.

Usage:
    cd reference/python-src && uv run python ../../scripts/gen_golden.py
"""

import asyncio
import datetime as _datetime_module
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
TESTDATA = ROOT / "testdata"
GOLDEN = TESTDATA / "golden"
REFERENCE = ROOT / "reference" / "python-src"

sys.path.insert(0, str(REFERENCE))

# News age is rendered as a relative string ("2 days ago"), computed against
# wall-clock "now" inside app.algorithms.news.format_news_age. Golden files
# must be reproducible no matter *when* this script runs, so "now" is frozen
# here to a fixed instant — the same one the Go test harness pins its clock
# to (see internal/algo/harness_test.go's goldenClock). If these two ever
# drift apart, every case touching an injured player's reasoning text fails
# with a "23 days ago" vs "6 days ago" style mismatch that has nothing to do
# with a real port bug.
GOLDEN_NOW = _datetime_module.datetime(2026, 7, 30, 12, 0, 0, tzinfo=_datetime_module.timezone.utc)


class _FrozenDateTime(_datetime_module.datetime):
    @classmethod
    def now(cls, tz=None):
        return GOLDEN_NOW if tz is not None else GOLDEN_NOW.replace(tzinfo=None)


def freeze_news_clock():
    from app.algorithms import news as _news

    _news.datetime = _FrozenDateTime

# ---------------------------------------------------------------------------
# Stub the FPL client before any algorithm imports it.
# ---------------------------------------------------------------------------

from app import fpl_client  # noqa: E402

_FIXTURES = {}


def _load(name):
    if name not in _FIXTURES:
        _FIXTURES[name] = json.loads((TESTDATA / f"{name}.json").read_text())
    return _FIXTURES[name]


# Fixed team_id used for the synthetic squad fixture (testdata/picks_squad1.json).
# Not a real FPL team — see scripts/make_squad_fixture.py and the plan's note
# on the entry/ data problem for why this fixture is synthetic rather than a
# captured real squad.
SYNTHETIC_TEAM_ID = 999001


def install_stub(bootstrap_name):
    """Point fpl_client at frozen payloads. Unknown paths fail loudly."""

    async def _fetch(path, ttl=None):
        if path == "/bootstrap-static/":
            return _load(bootstrap_name)
        if path == "/fixtures/":
            return _load("fixtures")
        if path == "/event-status/":
            return _load("event_status")
        if path == f"/entry/{SYNTHETIC_TEAM_ID}/event/1/picks/":
            return _load("picks_squad1")
        if path.startswith("/element-summary/") and path.endswith("/"):
            player_id = path.strip("/").split("/")[-1]
            return _load(f"player_summary_{player_id}")
        raise AssertionError(
            f"algorithm reached un-stubbed FPL path {path!r} — add a frozen "
            "payload for it before golden-filing this algorithm"
        )

    fpl_client._fetch = _fetch
    fpl_client._cache.clear()


# ---------------------------------------------------------------------------
# Cases: bootstrap+fixtures-only algorithms. Team-dependent ones (transfers,
# live, chips, scout, rivals, league_analyzer) need entry/ payloads that do not
# exist preseason and are golden-filed separately.
# ---------------------------------------------------------------------------


def cases():
    from app.algorithms.captain import get_captain_picks
    from app.algorithms.compare import compare_players
    from app.algorithms.differentials import get_differentials
    from app.algorithms.fixtures import get_fixture_outlook
    from app.algorithms.hit_analyzer import analyze_hit
    from app.algorithms.prices import get_price_predictions
    from app.algorithms.scout import get_squad_scout
    from app.algorithms.transfers import get_transfer_suggestions

    # Element ids are stable within the frozen bootstrap:
    #   411 Haaland (FWD 15.5)   426 B.Fernandes (MID 12.0)
    #   397 Semenyo (MID 8.5)      4 Gabriel (DEF 8.0)
    return [
        ("captain_gw1", get_captain_picks, {"gameweek": 1}),
        ("captain_default", get_captain_picks, {}),
        ("captain_top10", get_captain_picks, {"gameweek": 1, "top_n": 10}),
        ("differentials_10pct", get_differentials, {"max_ownership_pct": 10.0, "gameweek": 1}),
        ("differentials_5pct", get_differentials, {"max_ownership_pct": 5.0, "gameweek": 1}),
        ("fixtures_5gw", get_fixture_outlook, {"gameweeks_ahead": 5}),
        ("fixtures_3gw_mid", get_fixture_outlook, {"gameweeks_ahead": 3, "position": "MID"}),
        ("prices", get_price_predictions, {}),
        ("hit_swap_fwd_mid", analyze_hit, {"player_out_id": 411, "player_in_id": 426}),
        ("hit_swap_mid_def", analyze_hit, {"player_out_id": 397, "player_in_id": 4, "gameweeks_ahead": 3}),
        ("transfers_1ft", get_transfer_suggestions, {"team_id": SYNTHETIC_TEAM_ID, "free_transfers": 1, "bank_m": 0.5}),
        ("transfers_2ft", get_transfer_suggestions, {"team_id": SYNTHETIC_TEAM_ID, "free_transfers": 2, "bank_m": 0.0}),
        ("scout", get_squad_scout, {"team_id": SYNTHETIC_TEAM_ID}),
        (
            "compare_haaland_fernandes",
            compare_players,
            {"player_names": ["Haaland", "B.Fernandes"], "gameweeks_ahead": 4},
        ),
        (
            "compare_not_enough_names",
            compare_players,
            {"player_names": ["Haaland"], "gameweeks_ahead": 4},
        ),
        (
            "compare_no_match",
            compare_players,
            {"player_names": ["Haaland", "Nonexistentplayerxyz"], "gameweeks_ahead": 4},
        ),
    ]


async def run(bootstrap_name, suffix):
    install_stub(bootstrap_name)

    # Guard: captain.py loads weights at import time and silently falls back to
    # DEFAULT_WEIGHTS. If an optimized_weights.json ever appears in the
    # reference checkout, golden files would drift with no visible cause.
    from app.algorithms import captain

    if captain.WEIGHTS != captain.DEFAULT_WEIGHTS:
        raise SystemExit(
            "captain.WEIGHTS != DEFAULT_WEIGHTS — the reference checkout has "
            "optimized weights on disk. Remove reference/python-src/data/ and rerun."
        )

    written = []
    for name, fn, kwargs in cases():
        try:
            result = await fn(**kwargs)
        except TypeError as exc:
            print(f"  SKIP {name}: signature mismatch ({exc})")
            continue
        path = GOLDEN / f"{name}{suffix}.json"
        path.write_text(json.dumps(result, indent=2, sort_keys=True, default=str))
        written.append((path.name, path.stat().st_size))
    return written


def main():
    freeze_news_clock()
    GOLDEN.mkdir(parents=True, exist_ok=True)
    for bootstrap_name, suffix in [
        ("bootstrap_preseason", ""),
        ("bootstrap_midseason", "_mid"),
    ]:
        print(f"\n=== {bootstrap_name} ===")
        for fname, size in asyncio.run(run(bootstrap_name, suffix)):
            print(f"  {fname:<40} {size:>9,} bytes")


if __name__ == "__main__":
    main()
