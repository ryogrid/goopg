#!/usr/bin/env python3
"""ledger-triage.py — bulk-triage tool for .ralph/deferral_ledger.md (M0144-0010).

~2,100 open ledger rows cannot be triaged row-by-row inside tasks
(METHODOLOGY4/03-forward-plan §6). This tool is the periodic bulk-triage
pass owned by the M0119-successor cadence:

  (1) flags rows whose referenced code/tests no longer exist
      (file-existence + identifier membership + `git log` deletion /
      last-touch attribution — the `git log -S`-family check the task
      names),
  (2) folds same-mechanism rows into cluster rows (each row joins the
      mechanism area of its most-referenced repo file; no-file rows group
      by task-id family),
  (3) emits only the survivors for per-task triage.

READ-ONLY by contract: the ledger is append-only (harness R7,
scripts/ralph_protected_regions.py check_ledger); this tool never edits
it — it emits a triage REPORT. Human/M0119-style tasks then decide which
stale-candidates to mark `resolved` and which survivors to schedule.

Reference model (extracted from the landed / deferred / resume-point /
why cells):

  strong ref : a repo-side file reference (`*.go`, `*.sh`, `*.py`, `*.md`,
               `*.sql`, `*.txt`, `*.csv`, `*.json` — quoted or bare,
               with or without :line) or a `Test*` name.
  weak ref   : any other backtick-quoted identifier (function/type name).
  pg ref     : `.c`/`.h`/`.y`/`.l` refs and symbols found only in
               postgres/src — oracle citations (a port target). A pg ref
               never makes a row stale: upstream still exists to port.
               A pg ref gone even in postgres/ (e.g. `pg_hba.c` → `hba.c`)
               is reported as an annotation only.

Classification per row:

  stale-candidate : every resolvable ref is gone at HEAD AND (>=1 strong
                    ref gone, or >=3 weak refs gone) — the row's resume
                    point dangles.
  partial-stale   : >=1 strong ref gone and >=1 ref of any kind live.
  live            : all refs present.
  unverifiable    : no extractable ref, or only pg-side refs — survivor
                    by default (absence of a citation is not staleness).

usage: scripts/ledger-triage.py [--ledger PATH] [--out FILE]
                               [--no-attribution] [--full]
"""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from collections import defaultdict

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
LEDGER = os.path.join(REPO, ".ralph", "deferral_ledger.md")

PG_EXTS = (".c", ".h", ".y", ".l")
REPO_EXTS = (".go", ".sh", ".py", ".md", ".sql", ".txt", ".csv", ".json")

FILE_RE = re.compile(
    r"((?:[\w.+-]+/)*[\w.+-]+\.(?:go|c|h|y|l|sql|sh|py|md|txt|csv|json))(?::\d+)?")
SYM_RE = re.compile(r"`([A-Za-z_][A-Za-z0-9_]{2,})(?::\d+)?`")
TEST_RE = re.compile(r"\b(Test[A-Za-z0-9_]{3,})\b")

# Backtick tokens that are not code symbols — SQL keywords, GUC names,
# error codes, common literals.
SYM_SKIP = re.compile(
    r"^(?:select|insert|update|delete|from|where|and|or|not|null|true|false|"
    r"begin|commit|rollback|abort|end|set|show|explain|analyze|verbose|"
    r"create|drop|alter|table|index|view|type|schema|database|function|"
    r"primary|foreign|references|unique|check|default|constraint|key|"
    r"join|inner|outer|left|right|full|cross|on|using|as|order|by|group|"
    r"having|limit|offset|union|intersect|except|all|distinct|case|when|"
    r"then|else|exists|any|some|array|row|cast|values|into|returning|"
    r"copy|to|stdout|stdin|csv|header|format|delimiter|"
    r"enable_\w+|work_mem|shared_buffers|statement_timeout|maintenance_work_mem|"
    r"max_parallel_\w+|io_method|io_combine_limit|fsync|synchronous_commit|"
    r"[0-9a-f]{7,}|XX000|\d{2}[A-Z]\d{3}|\d{5})$",
    re.IGNORECASE,
)


def parse_rows(path):
    """Yield (lineno, status, date, task_id, landed, deferred, resume, why)."""
    rows = []
    for i, ln in enumerate(open(path, encoding="utf-8", errors="replace"), 1):
        if not ln.startswith("|"):
            continue
        cells = [c.strip() for c in ln.rstrip("\n").split("|")[1:-1]]
        if not cells or cells[0] in ("status", ":--"):
            continue
        if len(cells) < 7:
            cells += [""] * (7 - len(cells))
        elif len(cells) > 7:
            cells = cells[:3] + [" | ".join(cells[3:-3])] + cells[-3:]
        rows.append((i, *cells[:7]))
    return rows


def extract_refs(text):
    """Return (file_refs, sym_refs, test_refs) from one cell blob."""
    files = {m.group(1) for m in FILE_RE.finditer(text)}
    syms, tests = set(), set()
    for m in SYM_RE.finditer(text):
        s = m.group(1)
        if "." in s or SYM_SKIP.match(s):
            continue
        syms.add(s)
    for m in TEST_RE.finditer(text):
        tests.add(m.group(1))
    return files, syms, tests


# ------------------------------------------------------------- tree index

def build_file_index():
    """basename -> [repo-relative paths]; repo-tracked files + postgres/ walk."""
    tracked = subprocess.run(["git", "ls-files"], cwd=REPO, capture_output=True,
                             text=True, check=True).stdout.splitlines()
    allp = list(tracked)
    pgroot = os.path.join(REPO, "postgres")
    for dirpath, dirs, names in os.walk(pgroot):
        dirs[:] = [d for d in dirs if d != ".git"]
        for n in names:
            allp.append(os.path.relpath(os.path.join(dirpath, n), REPO))
    idx = defaultdict(list)
    for p in allp:
        idx[os.path.basename(p)].append(p)
    return idx, set(allp)


def resolve_file(ref, idx, allp):
    """Ledger file ref -> (repo paths, is_pg_only)."""
    ref = ref.lstrip("./")
    cands = []
    if "/" in ref:
        if ref in allp:
            cands.append(ref)
        elif idx.get(os.path.basename(ref)):
            # `sym/file.go`-style glued refs: the tail basename still
            # identifies the mechanism file.
            cands.extend(idx[os.path.basename(ref)])
    else:
        cands.extend(idx.get(ref, []))
    return cands, bool(cands) and all(p.startswith("postgres/") for p in cands)


WORD_RE = re.compile(r"\b[A-Za-z_][A-Za-z0-9_]{2,}\b")


def harvest_idents(roots, exts):
    idents = set()
    for root in roots:
        for dirpath, dirs, names in os.walk(root):
            dirs[:] = [d for d in dirs if d != ".git"]
            for n in names:
                if not n.endswith(exts):
                    continue
                try:
                    with open(os.path.join(dirpath, n), encoding="utf-8",
                              errors="replace") as f:
                        idents.update(WORD_RE.findall(f.read()))
                except OSError:
                    pass
    return idents


# --------------------------------------------------------------- git attr

def attr_file(ref):
    """Last commit touching a (now-gone) file, by basename pathspec."""
    base = os.path.basename(ref.lstrip("./"))
    r = subprocess.run(
        ["git", "log", "--all", "--oneline", "-1", "--format=%h %ad %s",
         "--date=short", "--", f"**/{base}"],
        cwd=REPO, capture_output=True, text=True)
    return r.stdout.strip() or "no history"


def attr_sym(sym):
    """Last commit adding/removing a gone symbol (git log -S)."""
    r = subprocess.run(
        ["git", "log", "--all", "--oneline", "-S", sym, "-1",
         "--format=%h %ad %s", "--date=short"],
        cwd=REPO, capture_output=True, text=True)
    return r.stdout.strip() or "no history"


# ------------------------------------------------------------------- main

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ledger", default=LEDGER)
    ap.add_argument("--out", default="-")
    ap.add_argument("--no-attribution", action="store_true",
                    help="skip git-log last-touch attribution (faster)")
    ap.add_argument("--full", action="store_true",
                    help="emit every survivor row, not just the cluster table")
    args = ap.parse_args()

    rows = parse_rows(args.ledger)
    open_rows = [r for r in rows if r[1] in ("-", "open", "[!]")]
    other = defaultdict(int)
    for r in rows:
        if r[1] not in ("-", "open", "[!]"):
            other[r[1]] += 1

    idx, allp = build_file_index()
    goopg_idents = harvest_idents(
        [os.path.join(REPO, "internal"), os.path.join(REPO, "cmd")], (".go",))
    pg_idents = harvest_idents(
        [os.path.join(REPO, "postgres", "src")], PG_EXTS)

    recs = []   # (lineno,tid), cls, gone, live, pg_gone
    file_to_rows = defaultdict(set)
    for n, (lineno, status, date, tid, landed, deferred, resume, why) in \
            enumerate(open_rows):
        blob = " ".join([landed, deferred, resume, why])
        f_refs, s_refs, t_refs = extract_refs(blob)
        gone, live, pg_gone = [], [], []
        strong_gone = strong_live = 0
        for f in f_refs:
            if f.endswith(PG_EXTS):
                paths, _ = resolve_file(f, idx, allp)
                if not paths:
                    pg_gone.append(f)
                continue  # oracle citation — never a staleness vote
            paths, is_pg_only = resolve_file(f, idx, allp)
            if not paths:
                gone.append(f)
                strong_gone += 1
            elif is_pg_only:
                pass  # resolved only inside postgres/ — oracle citation
            else:
                live.append(f)
                strong_live += 1
                for p in paths:
                    # Cluster keys are code/data files only — a .md/.txt
                    # citation (fix_plan, design docs) is bookkeeping,
                    # not a mechanism area.
                    if (not p.startswith("postgres/")
                            and p.endswith((".go", ".sh", ".py", ".sql"))):
                        file_to_rows[p].add(n)
        for s in s_refs:
            if s in goopg_idents:
                live.append(s)
            elif s in pg_idents:
                pass
            else:
                gone.append(s)
        for t in t_refs:
            if t in goopg_idents:
                live.append(t)
                strong_live += 1
            elif t in pg_idents:
                pass
            else:
                gone.append(t)
                strong_gone += 1
        weak_gone = len(gone) - strong_gone
        if not gone and not live:
            cls = "unverifiable"
        elif live:
            cls = "partial-stale" if gone else "live"
        elif strong_gone >= 1 or weak_gone >= 3:
            cls = "stale-candidate"
        else:
            cls = "unverifiable"
        recs.append(((lineno, tid), cls, gone, live, pg_gone))

    # attribution for gone refs of flagged rows only
    attr = {}
    if not args.no_attribution:
        for (_, _), cls, gone, _l, _p in recs:
            if cls not in ("stale-candidate", "partial-stale"):
                continue
            for g in gone:
                if g in attr:
                    continue
                attr[g] = (attr_file(g) if os.path.splitext(g)[1]
                           in REPO_EXTS or "/" in g or g.endswith(".go")
                           else attr_sym(g))

    # ------------------------------------------------------------- clusters
    # Cluster assignment: each row joins the mechanism area of its most-
    # referenced repo file (global citation count as the area key). No
    # transitive union — chaining through shared files merged ~everything
    # into one mega-component. Rows with no repo file ref group by
    # task-id family.
    row_files = defaultdict(set)   # row -> resolved repo paths it cites
    for f, members in file_to_rows.items():
        for m in members:
            row_files[m].add(f)

    def family(tid):
        parts = tid.split("-")
        if re.match(r"^[Mm]\d{4}", parts[0]):
            return parts[0].upper()
        return "-".join(parts[:2]) if len(parts) > 1 else parts[0]

    clusters = defaultdict(list)
    for i in range(len(recs)):
        if row_files[i]:
            primary = max(row_files[i],
                          key=lambda f: (len(file_to_rows[f]), f))
            clusters[primary].append(i)
        else:
            clusters[f"family:{family(open_rows[i][3])}"].append(i)

    counts = defaultdict(int)
    for r in recs:
        counts[r[1]] += 1

    # ----------------------------------------------------------------- emit
    out = []
    w = out.append
    w("# Ledger bulk-triage report\n")
    w(f"ledger: `{os.path.relpath(args.ledger, REPO)}`  ")
    w(f"rows parsed: **{len(rows)}**  open (`-`/`open`/`[!]`): **{len(open_rows)}**  "
      + "  ".join(f"{k}: {v}" for k, v in sorted(other.items())))
    w("")
    w("## Classification\n")
    w("| class | rows | meaning |")
    w("|---|---|---|")
    meanings = {
        "stale-candidate": "every resolvable ref gone at HEAD — resume point dangles",
        "partial-stale": ">=1 strong ref gone, >=1 ref live",
        "live": "all refs present",
        "unverifiable": "no extractable ref / pg-refs only — survivor by default",
    }
    for cls in ("stale-candidate", "partial-stale", "live", "unverifiable"):
        w(f"| {cls} | {counts[cls]} | {meanings[cls]} |")
    w("")
    w("## Stale candidates (auto-flag)\n")
    w("| line | task-id | gone refs (last touch) |")
    w("|---|---|---|")
    for (lineno, tid), cls, gone, _live, pgg in recs:
        if cls != "stale-candidate":
            continue
        bits = "; ".join(f"`{g}` ← {attr.get(g, '?')}" for g in gone[:4])
        if len(gone) > 4:
            bits += f" (+{len(gone)-4} more)"
        if pgg:
            bits += " · pg-ref also gone: " + ",".join(f"`{g}`" for g in pgg[:3])
        w(f"| {lineno} | {tid} | {bits} |")
    w("")
    w("## Clusters (same-mechanism folds; rows with any survivor)\n")
    w("| cluster | members | stale | partial | live | unverifiable | sample task-ids |")
    w("|---|---|---|---|---|---|---|")
    for label, members in sorted(clusters.items(), key=lambda kv: -len(kv[1])):
        c = defaultdict(int)
        tids = []
        for m in members:
            c[recs[m][1]] += 1
            tids.append(recs[m][0][1])
        if len(members) == c["stale-candidate"]:
            continue
        sample = ", ".join(list(dict.fromkeys(tids))[:4])
        w(f"| {label} | {len(members)} | {c['stale-candidate']} | "
          f"{c['partial-stale']} | {c['live']} | {c['unverifiable']} | {sample} |")
    w("")
    if args.full:
        w("## Survivor rows (per-task triage input)\n")
        w("| line | task-id | class | deferred (truncated) |")
        w("|---|---|---|---|")
        for i, ((lineno, tid), cls, _g, _l, _p) in enumerate(recs):
            if cls == "stale-candidate":
                continue
            d = open_rows[i][5].replace("|", "\\|")[:120]
            w(f"| {lineno} | {tid} | {cls} | {d} |")
        w("")

    text = "\n".join(out)
    if args.out == "-":
        sys.stdout.write(text)
    else:
        with open(args.out, "w") as f:
            f.write(text)
        print(f"wrote {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
