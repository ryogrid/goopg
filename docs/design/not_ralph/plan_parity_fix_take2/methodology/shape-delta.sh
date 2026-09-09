#!/usr/bin/env bash
# shape-delta.sh OLD.norm.txt NEW.norm.txt
#
# Counts queries whose PLAN TEXT changed between two goopg captures, and
# separately how many of those changed in SHAPE (node structure) rather than
# only in estimate digits.
#
# Why this exists (R35 / K50): `pg-plan-parity-diff.py` normalises estimates
# OUT of its verdict (N1 strips `rows=`/`cost=`, N5 strips `::type`, N6
# compares quals by column+operator multiset, not literal values). So a round
# that changes estimates measures ZERO on the category counts BY CONSTRUCTION
# — R34 changed 18 TPC-DS plans and every category came back byte-identical.
# Category counts are the wrong instrument for an estimate round; this is the
# right one, and it is reported ALONGSIDE them, never instead.
set -uo pipefail
OLD="$1"; NEW="$2"
python3 - "$OLD" "$NEW" <<'PY'
import re, sys
def secs(p):
    t = open(p, encoding="utf-8", errors="replace").read()
    return {m.group(1): m.group(2)
            for m in re.finditer(r'^=== (\S+)$(.*?)(?=^=== \S+$|\Z)', t, re.S | re.M)}
def shape(body):
    # node structure only: indentation + node label, estimates and quals dropped
    out = []
    for ln in body.splitlines():
        m = re.match(r'^(\s*(?:->\s+)?)([A-Z][A-Za-z ]*?)\s+\(cost=', ln)
        if m:
            out.append((len(m.group(1)), m.group(2).strip()))
    return tuple(out)
a, b = secs(sys.argv[1]), secs(sys.argv[2])
keys = sorted(set(a) | set(b), key=lambda k: (len(k), k))
text_changed, shape_changed = [], []
for k in keys:
    if a.get(k) != b.get(k):
        text_changed.append(k)
        if shape(a.get(k, "")) != shape(b.get(k, "")):
            shape_changed.append(k)
print("SHAPE-DELTA: queries=%d text-changed=%d shape-changed=%d"
      % (len(keys), len(text_changed), len(shape_changed)))
print("  text-changed : %s" % " ".join(text_changed) if text_changed else "  text-changed : (none)")
print("  shape-changed: %s" % " ".join(shape_changed) if shape_changed else "  shape-changed: (none)")
PY
