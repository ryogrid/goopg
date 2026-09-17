#!/usr/bin/env python3
"""ralph-lineage-guard.py — mechanical lineage budget for .ralph/fix_plan.md (H3).

Compares a BASELINE fix_plan against a CANDIDATE fix_plan and fails (exit 1)
when the candidate:

  Rule A (lineage budget): adds a new OPEN (`[ ]`) descendant under a root
      whose last 5 completed (`[x]`) descendants, in file order, all carry
      `Movement: none` (a missing Movement line counts as none — only tasks
      with a `Parent:` field are descendants, so legacy tasks never count).
      Remedy: write an escalation block into the root, mark the root [!],
      select a task elsewhere.
  Rule B (lineage required): adds a task whose id matches M0137..M0143 or P0-
      without a `Parent:` line.
  Rule D (field hygiene — audit hole D): a NEWLY ADDED line may not
      (1) be a task's ONLY `Parent:` / `Movement: yes` declaration while
          written mid-sentence on a body line. The guard reads those fields
          only at the start of a body line or anywhere on the task's own title
          line, so a mid-line `... prefix match. Parent: M0141-S7.` silently
          escapes descendant counting (M0141-S7-cd-candidatepool did exactly
          this). A mid-line repetition of a field the task also declares
          readably is harmless prose and is not reported; nor is a mid-line
          `Movement: none`, which is what the guard already assumes; nor a
          field on a baseline task that USED to declare it readably (the owner
          reshaping an existing entry is not the loop gaming it).
      (2) claim `Movement: yes` without citing one of S3's three instruments on
          the SAME line — a `match` count, `CATEGORIES-EXCL-MATCH`, or
          `ea-ratchet` — together with a number. A non-conforming
          `Movement: yes` is additionally TREATED AS `none` for Rule A, so it
          cannot reset the lineage budget (three such lines in HEAD did).
      Only lines that are new in the candidate are reported, so pre-existing
      offenders in HEAD do not block unrelated edits (they already count as
      `Movement: none`).
  Rule C (frozen): the frozen set is a list of id PREFIXES taken from the
      BASELINE: every `[!]` task mentioning FROZEN contributes its id, plus the
      tokens of any `FROZEN-PREFIXES: A B C` line in the owner-only
      `## Current Priority` banner. A baseline task whose id starts with a
      frozen prefix may not change status, disappear, or (if it carried FROZEN)
      lose the FROZEN token. A new task may not have a frozen-prefixed id, a
      frozen-prefixed Parent / Parent-chain ancestor, or be nested by indent
      under a frozen-prefixed task.

Task grammar:  `<indent>- [ |x|!] **<TASK-ID>...`  (id ends at whitespace or `*`)
Body lines (until the next checkbox task line or `#` heading), or the task's
header line itself, may carry
  `Parent: <TASK-ID|none>` and `Movement: yes — <evidence>` / `Movement: none`.

Modes:
  (default)                 baseline = git show HEAD:.ralph/fix_plan.md,
                            candidate = working-tree .ralph/fix_plan.md
  --staged                  baseline = HEAD, candidate = index (git show :path)
  --baseline F --candidate F   explicit files (tests)
Exit 0 when candidate == baseline. stdlib only.
"""

import argparse
import re
import subprocess
import sys

PLAN = ".ralph/fix_plan.md"
BUDGET = 5
TASK_RE = re.compile(r"^(\s*)- \[([ xX!])\] \*\*([^\s*]+)")
HEAD_RE = re.compile(r"^#{1,6} ")
# Parent/Movement: at the start of a body line, or anywhere on the task's own
# header line (e.g. `- [ ] **P0-E4 — title.** Parent: none.`).
PARENT_RE = re.compile(r"^\s*Parent:\s*`?([^\s`]+)`?")
MOVE_RE = re.compile(r"^\s*Movement:\s*(\S+)")
PARENT_INLINE_RE = re.compile(r"(?:^|\s)Parent:\s*`?([^\s`]+)`?")
MOVE_INLINE_RE = re.compile(r"(?:^|\s)Movement:\s*(\S+)")
NEWID_RE = re.compile(r"^(M01(3[7-9]|4[0-3])|P0-)")
# Rule D: `Movement: yes` must cite an instrument with a number on the SAME
# line — a `match` count (this also covers CATEGORIES-EXCL-MATCH) or
# `ea-ratchet`.
MOVE_YES_RE = re.compile(r"(?:^|\s)Movement:\s*yes", re.I)
INSTRUMENT_RE = re.compile(r"\bmatch\b|CATEGORIES-EXCL-MATCH|\bea-ratchet\b", re.I)
NUMBER_RE = re.compile(r"[0-9]")


def movement_yes_conforms(line):
    """True when a `Movement: yes` line cites an instrument AND a number."""
    return bool(INSTRUMENT_RE.search(line)) and bool(NUMBER_RE.search(line))


class Task:
    __slots__ = ("id", "status", "indent", "parent", "has_parent", "movement",
                 "frozen", "line", "idx", "enclosing", "bad_move_lines",
                 "midline_lines")

    def __init__(self, tid, status, indent, line, idx):
        self.id, self.status, self.indent, self.line, self.idx = tid, status, indent, line, idx
        self.parent, self.has_parent, self.movement = None, False, None
        self.frozen, self.enclosing = False, None
        # Rule D bookkeeping: (lineno, raw line) pairs.
        self.bad_move_lines = []      # non-conforming `Movement: yes`
        self.midline_lines = []       # mid-sentence Parent:/Movement: on a body line


def parse(text):
    tasks, order = {}, []
    cur = None
    stack = []  # (indent, task) for indentation nesting
    for n, ln in enumerate(text.splitlines(), 1):
        mt = TASK_RE.match(ln)
        if mt:
            indent = len(mt.group(1).expandtabs(4))
            status = mt.group(2).lower()
            tid = mt.group(3).rstrip(".,:;—")
            cur = Task(tid, status, indent, n, len(order))
            if "FROZEN" in ln:
                cur.frozen = True
            _fields(cur, ln, n, True)
            while stack and stack[-1][0] >= indent:
                stack.pop()
            cur.enclosing = stack[-1][1].id if stack else None
            stack.append((indent, cur))
            if tid not in tasks:  # first occurrence wins
                tasks[tid] = cur
                order.append(cur)
            continue
        if HEAD_RE.match(ln):
            cur, stack = None, []
            continue
        if cur is None:
            continue
        if "FROZEN" in ln:
            cur.frozen = True
        _fields(cur, ln, n, False)
    return tasks, order


def _fields(cur, ln, n, inline):
    """Read Parent:/Movement: off one line. `inline` is True for the task's own
    title line (fields may sit anywhere on it) and False for a body line (only
    line-start fields are read — Rule D reports the rest)."""
    pre = PARENT_INLINE_RE if inline else PARENT_RE
    mre = MOVE_INLINE_RE if inline else MOVE_RE
    mp = pre.search(ln) if inline else pre.match(ln)
    if mp and not cur.has_parent:
        cur.has_parent = True
        p = mp.group(1).rstrip(".,;")
        cur.parent = None if p.lower() == "none" else p
    mm = mre.search(ln) if inline else mre.match(ln)
    if mm and cur.movement is None:
        val = mm.group(1).lower().rstrip(".,;—")
        # Rule D(2): `Movement: yes` with no instrument+number is treated as
        # `none`, so it cannot reset the Rule A lineage budget.
        if val.startswith("yes") and not movement_yes_conforms(ln):
            val = "none"
        cur.movement = val
    # Rule D bookkeeping (reported only for lines new in the candidate).
    if MOVE_YES_RE.search(ln) and not movement_yes_conforms(ln):
        cur.bad_move_lines.append((n, ln))
    elif not inline:
        # A mid-sentence field the readers above could not see. Reported only
        # when the task has NO readable field of that kind (the actual gaming
        # vector: M0141-S7-cd-candidatepool's only `Parent:` was mid-sentence).
        # A mid-line repetition of a field already declared at line start / on
        # the title line is harmless prose.
        if PARENT_INLINE_RE.search(ln) and not PARENT_RE.match(ln):
            cur.midline_lines.append((n, ln, "Parent"))
        mmid = MOVE_INLINE_RE.search(ln)
        if mmid and not MOVE_RE.match(ln) and \
                mmid.group(1).lower().rstrip(".,;\u2014").startswith("yes"):
            # A mid-line `Movement: none` is what the guard already assumes, so
            # it changes nothing; only an unread `Movement: yes` CLAIM matters.
            cur.midline_lines.append((n, ln, "Movement"))


FROZEN_PREFIXES_TOKEN = "FROZEN-PREFIXES:"
BANNER_HEADING = "## Current Priority"


def banner_lines(text):
    """Lines of the owner-only `## Current Priority` banner (up to next `## `)."""
    out, inside = [], False
    for ln in text.splitlines():
        if not inside:
            if ln.rstrip() == BANNER_HEADING:
                inside = True
            continue
        if ln.startswith("## "):
            break
        out.append(ln)
    return out


def frozen_prefixes(text):
    """Id prefixes from `FROZEN-PREFIXES: A B C` lines in the banner."""
    out = []
    for ln in banner_lines(text):
        i = ln.find(FROZEN_PREFIXES_TOKEN)
        if i < 0:
            continue
        for tok in ln[i + len(FROZEN_PREFIXES_TOKEN):].split():
            tok = tok.strip("`*,;.")
            if tok:
                out.append(tok)
    return out


def root_of(tasks, t):
    seen = set()
    while t.parent and t.parent in tasks and t.parent not in seen:
        seen.add(t.id)
        t = tasks[t.parent]
    return t.id


def ancestors(tasks, t):
    out, seen = [], {t.id}
    while t.parent and t.parent in tasks and t.parent not in seen:
        seen.add(t.parent)
        t = tasks[t.parent]
        out.append(t.id)
    return out


def check(baseline, candidate):
    if baseline == candidate:
        return []
    btasks, _ = parse(baseline)
    ctasks, corder = parse(candidate)
    new = [t for t in corder if t.id not in btasks]
    errs = []

    # Rule B
    for t in new:
        if NEWID_RE.match(t.id) and not t.has_parent:
            errs.append(
                f"[B] new task {t.id} (line {t.line}) has no `Parent:` line. Every new "
                f"M0137-M0143 / P0- task names the task that filed it (`Parent: <TASK-ID>`, "
                f"or `Parent: none` for owner-filed tasks).")

    # Rule A
    completed_by_root = {}
    for t in corder:
        if t.parent is None or t.parent not in ctasks:
            continue
        if t.status == "x":
            completed_by_root.setdefault(root_of(ctasks, t), []).append(t)
    exhausted = {}
    for root, done in completed_by_root.items():
        last = done[-BUDGET:]
        if len(last) == BUDGET and all((d.movement or "none").startswith("none") for d in last):
            exhausted[root] = last
    for t in new:
        if t.status != " " or t.parent is None or t.parent not in ctasks:
            continue
        root = root_of(ctasks, t)
        if root in exhausted:
            ids = ", ".join(d.id for d in exhausted[root])
            errs.append(
                f"[A] new open task {t.id} (line {t.line}) descends from root {root}, whose "
                f"last {BUDGET} completed descendants ({ids}) all show `Movement: none`. "
                f"Lineage budget exhausted: do NOT file/select another descendant. Write an "
                f"escalation block into {root} (what was attempted, what each step proved, "
                f"the remaining blocker, expected movement if unblocked, remaining size), "
                f"mark {root} [!], and select a task elsewhere. Only the owner re-opens it.")

    # Rule C
    frozen_ids = {t.id for t in btasks.values() if t.status == "!" and t.frozen}
    prefixes = sorted(frozen_ids | set(frozen_prefixes(baseline)))

    def fz(tid):
        return next((p for p in prefixes if tid and tid.startswith(p)), None)

    for bt in sorted(btasks.values(), key=lambda t: t.idx):
        p = fz(bt.id)
        if p is None:
            continue
        ct = ctasks.get(bt.id)
        if ct is None:
            errs.append(
                f"[C] frozen task {bt.id} (frozen prefix {p}) disappeared from the plan. "
                f"Frozen lineages are archived/re-opened only by the owner; restore it.")
            continue
        if ct.status != bt.status:
            errs.append(
                f"[C] frozen task {bt.id} (frozen prefix {p}) changed from [{bt.status}] to "
                f"[{ct.status}] (line {ct.line}). Frozen tasks are re-opened only by the owner.")
        if bt.frozen and not ct.frozen:
            errs.append(
                f"[C] frozen task {bt.id} lost its FROZEN token (line {ct.line}). "
                f"Only the owner lifts a freeze; keep the FROZEN marker.")
    if prefixes:
        for t in new:
            p = fz(t.id)
            if p is not None:
                errs.append(
                    f"[C] new task {t.id} (line {t.line}) is inside frozen lineage prefix {p}. "
                    f"Do not extend a frozen lineage; select elsewhere or escalate.")
                continue
            hit = [a for a in ancestors(ctasks, t) if fz(a)]
            if t.parent and fz(t.parent) and t.parent not in hit:
                hit.insert(0, t.parent)
            if t.enclosing and fz(t.enclosing):
                hit.append(t.enclosing)
            if hit:
                errs.append(
                    f"[C] new task {t.id} (line {t.line}) is a child of frozen task "
                    f"{hit[0]} (frozen prefix {fz(hit[0])}). Do not extend a frozen lineage; "
                    f"select elsewhere or escalate.")

    # Rule D — field hygiene, NEWLY ADDED lines only.
    base_lines = {ln.rstrip() for ln in baseline.splitlines()}
    for t in corder:
        for n, ln in t.bad_move_lines:
            if ln.rstrip() in base_lines:
                continue
            errs.append(
                f"[D] task {t.id} (line {n}) claims `Movement: yes` without citing an "
                f"instrument on the same line. `Movement: yes` must name one of S3's three "
                f"instruments WITH a number: a `match` count change, `CATEGORIES-EXCL-MATCH`, "
                f"or `ea-ratchet` (e.g. `Movement: yes — match 539 -> 530`). Until then it "
                f"counts as `Movement: none` and does NOT reset the lineage budget. Line: "
                f"{ln.strip()[:120]}")
        for n, ln, field in t.midline_lines:
            if ln.rstrip() in base_lines:
                continue
            bt = btasks.get(t.id)
            if field == "Parent":
                if t.has_parent:
                    continue      # already declared readably elsewhere
                if bt is not None and bt.has_parent:
                    continue      # a baseline task that LOST a readable field
            else:
                if t.movement is not None:
                    continue
                if bt is not None and bt.movement is not None:
                    continue
            errs.append(
                f"[D] task {t.id} (line {n}) declares `{field}:` mid-sentence on a body line, "
                f"where the guard does not read it, and the task declares it nowhere else "
                f"(descendant counting and the lineage budget both miss it). Put the field at "
                f"the START of its own line, or on the task's title line. "
                f"Line: {ln.strip()[:120]}")
    return errs


def git_show(spec):
    r = subprocess.run(["git", "show", spec], capture_output=True)
    if r.returncode != 0:
        return ""
    return r.stdout.decode("utf-8", errors="surrogateescape")


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    ap.add_argument("--staged", action="store_true")
    ap.add_argument("--baseline")
    ap.add_argument("--candidate")
    ap.add_argument("--path", default=PLAN)
    a = ap.parse_args(argv)
    if a.baseline or a.candidate:
        if not (a.baseline and a.candidate):
            ap.error("--baseline and --candidate go together")
        with open(a.baseline, encoding="utf-8", errors="surrogateescape") as f:
            base = f.read()
        with open(a.candidate, encoding="utf-8", errors="surrogateescape") as f:
            cand = f.read()
    else:
        base = git_show("HEAD:" + a.path)
        if a.staged:
            cand = git_show(":" + a.path)
        else:
            try:
                with open(a.path, encoding="utf-8", errors="surrogateescape") as f:
                    cand = f.read()
            except OSError:
                cand = ""
        if not cand:  # file absent/deleted: nothing to police here
            return 0
    errs = check(base, cand)
    if errs:
        sys.stderr.write("ralph-lineage-guard: %d violation(s) in %s\n" % (len(errs), a.path))
        for e in errs:
            sys.stderr.write("  " + e + "\n")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
