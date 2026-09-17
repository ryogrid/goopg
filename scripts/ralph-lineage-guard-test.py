#!/usr/bin/env python3
"""Self-test for scripts/ralph-lineage-guard.py (synthetic fix_plan fixtures).
Usage: python3 scripts/ralph-lineage-guard-test.py   (exit 0 = all pass)"""

import importlib.util
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("lg", os.path.join(HERE, "ralph-lineage-guard.py"))
lg = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lg)

HEADER = "# Fix plan\n\n## Current Priority\n1. x\n\n## M0142 — stuff\n\n"


def task(tid, status=" ", parent=None, movement=None, indent="", extra=""):
    s = f"{indent}- [{status}] **{tid} — title {extra}**\n{indent}  body text\n"
    if parent is not None:
        s += f"{indent}  Parent: {parent}\n"
    if movement is not None:
        s += f"{indent}  Movement: {movement}\n"
    return s


def plan(*tasks):
    return HEADER + "".join(tasks)


ROOT = task("M0142-0008", "x", extra="legacy root")


def chain(n, movement="none"):
    return [task(f"M0142-0008a-{i}", "x", parent="M0142-0008" if i == 0 else f"M0142-0008a-{i-1}",
                 movement=movement) for i in range(n)]


cases = []


def case(name, base, cand, want):
    cases.append((name, base, cand, want))


# --- Rule A -----------------------------------------------------------------
b = plan(ROOT, *chain(5))
case("A: 5 none-movement + new open child -> violation", b,
     b + task("M0142-0008a-5", " ", parent="M0142-0008a-4"), "A")
case("A: new child attached directly to exhausted root", b,
     b + task("M0142-0008b", " ", parent="M0142-0008"), "A")
b4 = plan(ROOT, *chain(4))
case("A: only 4 completed -> ok", b4, b4 + task("M0142-0008a-4", " ", parent="M0142-0008a-3"), None)
cm = chain(5)
cm[2] = task("M0142-0008a-2", "x", parent="M0142-0008a-1", movement="yes — Q3 MATCH")
bm = plan(ROOT, *cm)
case("A: one movement:yes in last 5 -> ok", bm, bm + task("M0142-0008a-5", " ", parent="M0142-0008a-4"), None)
cmiss = chain(5)
cmiss[4] = task("M0142-0008a-4", "x", parent="M0142-0008a-3")  # Movement missing -> none
bmiss = plan(ROOT, *cmiss)
case("A: missing Movement counts as none", bmiss,
     bmiss + task("M0142-0008a-5", " ", parent="M0142-0008a-4"), "A")
c6 = chain(6)
c6[0] = task("M0142-0008a-0", "x", parent="M0142-0008", movement="yes — early win")
b6 = plan(ROOT, *c6)
case("A: movement older than last 5 does not help", b6,
     b6 + task("M0142-0008a-6", " ", parent="M0142-0008a-5"), "A")
case("A: existing open task in baseline is not 'new' -> ok",
     b + task("M0142-0008a-5", " ", parent="M0142-0008a-4"),
     b + task("M0142-0008a-5", " ", parent="M0142-0008a-4", extra="reworded"), None)
case("A: new task under a different root -> ok", b,
     b + task("M0141-0001", "x", parent="none") + task("M0141-0001a", " ", parent="M0141-0001"), None)
case("A: exhausted lineage but new task is [!] escalation -> ok", b,
     b + task("M0142-0008a-5", "!", parent="M0142-0008a-4"), None)

# --- Rule B -----------------------------------------------------------------
base = plan(ROOT)
case("B: new M0142 task without Parent -> violation", base, base + task("M0142-0020"), "B")
case("B: new P0- task without Parent -> violation", base, base + task("P0-E4"), "B")
case("B: new M0143 task with Parent: none -> ok", base, base + task("M0143-0009", parent="none"), None)
case("B: new M0136 (out of range) without Parent -> ok", base, base + task("M0136-0100"), None)
case("B: new nested M0141 subtask without Parent -> violation", base,
     base + task("M0141-S2b-8", indent="  "), "B")
case("B: legacy task edited (not new) without Parent -> ok",
     plan(ROOT, task("M0140-0006a")), plan(ROOT, task("M0140-0006a", "x")), None)

case("B: Parent inline on the header line -> ok", base,
     base + "- [ ] **P0-E4 — reproduce.** Parent: none.\n  body\n", None)
case("B: Parent inline P0-E5 continuation line -> ok", base,
     base + "- [ ] **P0-E5 — fix.**\n  Parent: none. Depends on P0-E4.\n", None)
case("A: inline Parent/Movement on header count for lineage",
     plan(ROOT, *["- [x] **M0142-0008a-%d — t** Parent: %s Movement: none\n" % (i, "M0142-0008" if i == 0 else "M0142-0008a-%d" % (i - 1)) for i in range(5)]),
     plan(ROOT, *["- [x] **M0142-0008a-%d — t** Parent: %s Movement: none\n" % (i, "M0142-0008" if i == 0 else "M0142-0008a-%d" % (i - 1)) for i in range(5)])
     + "- [ ] **M0142-0008a-5 — t** Parent: M0142-0008a-4\n", "A")

# --- Rule C -----------------------------------------------------------------
fz = task("M0142-0008c", "!", parent="none", extra="FROZEN pending Q2 decision")
bf = plan(ROOT, fz)
case("C: frozen [!] -> [ ] violation", bf,
     plan(ROOT, task("M0142-0008c", " ", parent="none", extra="FROZEN pending Q2 decision")), "C")
case("C: frozen [!] -> [x] violation", bf,
     plan(ROOT, task("M0142-0008c", "x", parent="none", extra="unfrozen")), "C")
case("C: new child via Parent -> violation", bf, bf + task("M0142-0008c-1", parent="M0142-0008c"), "C")
case("C: new grandchild via Parent chain -> violation",
     bf + task("M0142-0008c-1", "x", parent="M0142-0008c", movement="yes — x"),
     bf + task("M0142-0008c-1", "x", parent="M0142-0008c", movement="yes — x")
     + task("M0142-0008c-1a", parent="M0142-0008c-1"), "C")
case("C: new child nested by indentation (legacy, no Parent) -> violation",
     plan(task("M0100-0001", "!", extra="FROZEN")),
     plan(task("M0100-0001", "!", extra="FROZEN"), task("M0100-0001a", indent="  ")), "C")
case("C: FROZEN word in body (not header) still counts",
     plan("- [!] **M0100-0002 — t**\n  FROZEN by owner 2026-09-17\n"),
     plan("- [ ] **M0100-0002 — t**\n  FROZEN by owner 2026-09-17\n"), "C")
case("C: non-frozen [!] may reopen -> ok",
     plan(task("M0100-0003", "!", extra="blocked")), plan(task("M0100-0003", " ", extra="blocked")), None)
case("C: frozen task body text edit -> ok", bf,
     plan(ROOT, task("M0142-0008c", "!", parent="none", extra="FROZEN pending Q2 decision (note)")), None)

# --- Rule C: frozen prefixes --------------------------------------------------
FPH = "# Fix plan\n\n## Current Priority\n1. x\nFROZEN-PREFIXES: M0142-0008a-3 M0142-0008c-1a\n\n## M0142 — stuff\n\n"


def fplan(*tasks):
    return FPH + "".join(tasks)


a3 = task("M0142-0008a-3", "x", parent="M0142-0008", movement="none")
a3i = task("M0142-0008a-3-i", "x", parent="M0142-0008a-3", movement="none")
bp = fplan(ROOT, a3, a3i)
case("C: banner prefix — new task id under prefix -> violation", bp,
     bp + task("M0142-0008a-3-iv", " ", parent="M0142-0008"), "C")
case("C: banner prefix — new task whose Parent is frozen-prefixed -> violation", bp,
     bp + task("M0142-0008z", " ", parent="M0142-0008a-3-i"), "C")
case("C: banner prefix — [x] task under prefix reopened -> violation", bp,
     fplan(ROOT, task("M0142-0008a-3", " ", parent="M0142-0008", movement="none"), a3i), "C")
case("C: banner prefix — task under prefix disappears -> violation", bp, fplan(ROOT, a3), "C")
case("C: banner prefix — body edit of frozen-prefixed task -> ok", bp,
     fplan(ROOT, task("M0142-0008a-3", "x", parent="M0142-0008", movement="none", extra="(note)"), a3i), None)
case("C: banner prefix — sibling outside prefix -> ok", bp,
     bp + task("M0142-0008a-4", " ", parent="M0142-0008"), None)
case("C: FROZEN-PREFIXES outside the banner is ignored",
     plan(ROOT, a3) + "FROZEN-PREFIXES: M0142-0008a-3\n",
     plan(ROOT, a3) + "FROZEN-PREFIXES: M0142-0008a-3\n" + task("M0142-0008a-3-ii", " ", parent="M0142-0008"), None)
case("C: prefix list comes from BASELINE (candidate cannot add/remove)",
     bp, bp.replace(" M0142-0008a-3 ", " ") + task("M0142-0008a-3-v", " ", parent="none"), "C")
case("C: [!] FROZEN id is itself a prefix — new id extending it -> violation", bf,
     bf + task("M0142-0008c-9", " ", parent="none"), "C")
case("C: frozen task loses FROZEN token -> violation", bf,
     plan(ROOT, task("M0142-0008c", "!", parent="none", extra="pending Q2 decision")), "C")
case("C: frozen task disappears -> violation", bf, plan(ROOT), "C")
case("C: prefix match by startswith (banner M0142-0008c-1a covers -1a-x)",
     fplan(ROOT), fplan(ROOT) + task("M0142-0008c-1a-x", " ", parent="none"), "C")

# --- Rule D: field hygiene (audit hole D) -----------------------------------
# (1) Parent:/Movement: written mid-sentence on a body line is invisible to the
#     guard, which is how M0141-S7-cd-candidatepool escaped descendant counting.
case("D: mid-line Parent on a body line -> violation", base,
     base + "- [ ] **M0142-0040 — t**\n  narrowed to a prefix match. Parent: M0142-0008.\n", "D")
case("D: mid-line Parent still leaves the task parentless (Rule B too)", base,
     base + "- [ ] **M0143-0041 — t**\n  narrowed to a prefix match. Parent: M0143-0009.\n", "B")
case("D: Parent at the start of a body line -> ok", base,
     base + "- [ ] **M0142-0042 — t**\n  Parent: M0142-0008\n", None)
case("D: Parent on the task's own title line -> ok", base,
     base + "- [ ] **M0142-0043 — t.** Parent: M0142-0008.\n  body\n", None)
case("D: mid-line Movement: yes claim on a body line -> violation", base,
     base + "- [x] **M0142-0044 — t**\n  Parent: M0142-0008\n  it shipped: Movement: yes — match 539 -> 530 here.\n", "D")
case("D: mid-line Movement: none changes nothing -> ok", base,
     base + "- [x] **M0142-0047 — t**\n  Parent: M0142-0008\n  it shipped, so Movement: none here.\n", None)
case("D: a pre-existing mid-line Parent is not re-reported",
     base + "- [ ] **M0142-0045 — t**\n  a prefix match. Parent: M0142-0008.\n",
     base + "- [ ] **M0142-0045 — t**\n  a prefix match. Parent: M0142-0008.\n"
     + task("M0142-0046", " ", parent="M0142-0008"), None)

# (2) `Movement: yes` must cite one of S3's three instruments WITH a number.
NC = "- [x] **M0142-0050 — t**\n  Parent: M0142-0008\n  Movement: yes — a same-named composite type declared in two schemas now resolves.\n"
case("D: Movement: yes with no instrument -> violation", base, base + NC, "D")
case("D: Movement: yes citing a match count -> ok", base,
     base + "- [x] **M0142-0051 — t**\n  Parent: M0142-0008\n  Movement: yes — match 539 -> 530\n", None)
case("D: Movement: yes citing CATEGORIES-EXCL-MATCH -> ok", base,
     base + "- [x] **M0142-0052 — t**\n  Parent: M0142-0008\n  Movement: yes — CATEGORIES-EXCL-MATCH 21 -> 19\n", None)
case("D: Movement: yes citing ea-ratchet -> ok", base,
     base + "- [x] **M0142-0053 — t**\n  Parent: M0142-0008\n  Movement: yes — ea-ratchet 140 -> 131 nodes\n", None)
case("D: Movement: yes naming an instrument but no number -> violation", base,
     base + "- [x] **M0142-0054 — t**\n  Parent: M0142-0008\n  Movement: yes — the match count moved\n", "D")
case("D: a pre-existing non-conforming Movement: yes is not re-reported",
     base + NC, base + NC + task("M0142-0055", " ", parent="M0142-0008"), None)

# (2b) a non-conforming `Movement: yes` counts as `none`, so it cannot reset the
#      lineage budget (three such lines silently did in HEAD).
NCCHAIN = plan(ROOT, *[
    "- [x] **M0142-0008a-%d — t**\n  Parent: %s\n  Movement: yes — the plan looks better now.\n"
    % (i, "M0142-0008" if i == 0 else "M0142-0008a-%d" % (i - 1)) for i in range(5)])
case("D: 5 non-conforming Movement: yes do NOT reset the budget", NCCHAIN,
     NCCHAIN + task("M0142-0008a-5", " ", parent="M0142-0008a-4"), "A")
CONFCHAIN = plan(ROOT, *[
    "- [x] **M0142-0008a-%d — t**\n  Parent: %s\n  Movement: yes — match 100 -> %d\n"
    % (i, "M0142-0008" if i == 0 else "M0142-0008a-%d" % (i - 1), 99 - i) for i in range(5)])
case("D: 5 conforming Movement: yes keep the budget open", CONFCHAIN,
     CONFCHAIN + task("M0142-0008a-5", " ", parent="M0142-0008a-4"), None)

# --- misc -------------------------------------------------------------------
case("unchanged -> ok", b, b, None)
case("heading ends a task body (Parent after heading ignored)",
     base, base + "- [ ] **M0142-0030 — t**\n\n## Next\n  Parent: none\n", "B")

fail = 0
for name, base_t, cand_t, want in cases:
    errs = lg.check(base_t, cand_t)
    rules = {e[1] for e in errs}
    ok = (not errs) if want is None else (want in rules)
    if not ok:
        fail += 1
        print(f"FAIL {name}: want={want} got={sorted(rules)}")
        for e in errs:
            print("   ", e)
print(f"ralph-lineage-guard-test: {len(cases) - fail} passed, {fail} failed")
sys.exit(1 if fail else 0)
