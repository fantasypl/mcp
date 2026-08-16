"""
Build the `midseason` test fixture.

The live preseason bootstrap carries last season's totals (points_per_game,
minutes, ict_index, xG/90 ...) but resets `form` to 0.0 for every player.
That leaves two code paths untested by golden files:

  - the captain model's `form` term (weight 3.43, second-largest)
  - detect_streak(), which short-circuits to "neutral" whenever form <= 0

So we take the real preseason bootstrap and surgically overwrite `form` (and
the `chance_of_playing_next_round` / `status` spread) from vaastav's 2025-26
players_raw.csv, matched on the permanent FPL player `code`. Everything else —
team ids, element ids, fixture joins, element_types — stays untouched, so the
fixture still joins cleanly against the real fixtures.json.

Usage: uv run --with requests python scripts/make_testdata.py
"""

import csv
import json
import pathlib
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
TESTDATA = ROOT / "testdata"
VAASTAV = (
    "https://raw.githubusercontent.com/vaastav/Fantasy-Premier-League"
    "/HEAD/data/2025-26/players_raw.csv"
)

# Fields we lift from the historical snapshot, with the type real FPL actually
# emits for each. This matters: vaastav round-trips through CSV, so everything
# arrives as a string. Injecting a string where FPL emits an int would bake a
# fabricated type into the golden files and force the Go port to reproduce a
# behavior that never occurs in production.
#
# Verified against the live bootstrap:
#   form, value_form, ep_this, status  -> str   ("6.2", "a")
#   chance_of_playing_next_round       -> int or null  (0, 75, 100)
INJECT = {
    "form": str,
    "value_form": str,
    "ep_this": str,
    "status": str,
    "chance_of_playing_next_round": int,
}


def coerce(s, typ):
    """vaastav writes missing values as '' or the literal string 'None'."""
    if s is None or s == "" or s == "None":
        return None
    if typ is int:
        return int(float(s))
    return str(s)


def main():
    bootstrap = json.loads((TESTDATA / "bootstrap_preseason.json").read_text())

    with urllib.request.urlopen(VAASTAV) as r:
        rows = list(csv.DictReader(r.read().decode().splitlines()))

    by_code = {int(r["code"]): r for r in rows if r.get("code")}
    print(f"historical rows: {len(rows)}  (unique codes: {len(by_code)})")

    hits = 0
    with_form = 0
    for el in bootstrap["elements"]:
        src = by_code.get(el.get("code"))
        if not src:
            continue
        hits += 1
        for field, typ in INJECT.items():
            v = coerce(src.get(field), typ)
            if v is not None:
                el[field] = v
        if float(el.get("form") or 0) > 0:
            with_form += 1

    total = len(bootstrap["elements"])
    print(f"matched on code: {hits}/{total}")
    print(f"nonzero form after injection: {with_form}/{total}")

    if with_form < 100:
        raise SystemExit(
            f"only {with_form} players have nonzero form — fixture would not "
            "meaningfully exercise the form term; aborting."
        )

    out = TESTDATA / "bootstrap_midseason.json"
    out.write_text(json.dumps(bootstrap, separators=(",", ":"), sort_keys=True))
    print(f"wrote {out} ({out.stat().st_size:,} bytes)")


if __name__ == "__main__":
    main()
