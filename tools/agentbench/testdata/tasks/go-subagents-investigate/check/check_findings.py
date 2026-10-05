import json
import re
import sys

# The failing test, the function with the bug, and its file, per package.
want = {
    "pricing": ("TestDiscountRoundsToNearestCent", "percentOf", "pricing/discount.go"),
    "inventory": ("TestReserveTheLastUnits", "canReserve", "inventory/reserve.go"),
    "schedule": ("TestNextRunOnSunday", "onDay", "schedule/days.go"),
}


def name(s):
    # "pricing.percentOf", "(*Stock).canReserve", or "percentOf()" name percentOf.
    return re.sub(r"\(.*?\)", "", str(s or "")).strip("` ").split(".")[-1]


def path(s):
    return str(s or "").strip("` ").removeprefix("./")


try:
    got = json.load(open(sys.argv[1]))
except Exception as e:
    sys.exit(f"cannot read {sys.argv[1]}: {e}")
bad = 0
for pkg, (test, fn, file) in want.items():
    g = got.get(pkg) or {}
    if name(g.get("test")) != test or name(g.get("function")) != fn or path(g.get("file")) != file:
        print(f"{pkg}: got {g}, want test={test} function={fn} file={file}")
        bad += 1
sys.exit(1 if bad else 0)
