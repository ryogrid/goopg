#!/usr/bin/env python3
"""Protected-region guard for the Ralph loop's instruction files (H4).

The loop (RALPH_LOOP=1) may not edit:
  * any CLAUDE.md                                   (whole file)
  * AGENT.md between the harness markers            (marker lines included)
      <!-- PLAN-PARITY-HARNESS:BEGIN --> ... <!-- PLAN-PARITY-HARNESS:END -->
  * .ralph/fix_plan.md banner region                (heading line included)
      from the line `## Current Priority` up to (not incl.) the next `## ` line
  * harness mechanism files                         (whole file; HARNESS_SUFFIXES,
      scripts/ralph-*guard*, .githooks/*, anything under ~/.ralph/)
      — this includes .ralph/gate-exceptions.md, the owner-only table that
      decides when a SKIP-BLOCKED gate stamp may be committed

Two entry points share the region logic so they cannot drift:

  ralph_protected_regions.py file-guard
      Claude Code PreToolUse hook for Edit|Write|MultiEdit|NotebookEdit|
      mcp__serena__.*. Reads the hook
      JSON on stdin, simulates the edit against the current file, and denies
      it when a protected region would change. No-op unless RALPH_LOOP=1.

  ralph_protected_regions.py check-staged
      Used by .githooks/pre-commit (caller decides RALPH_LOOP gating).
      Compares protected regions of `git show HEAD:path` vs `git show :path`
      for every staged path; exit 1 on a change.

  ralph_protected_regions.py check-ledger
      Used by .githooks/pre-commit: .ralph/deferral_ledger.md rows are
      append-only; appended rows may not carry OWNER DECISION nor re-use the
      task-id of an OWNER DECISION row.

  ralph_protected_regions.py check-designdocs
      Used by .githooks/pre-commit: mechanises AGENT.md rule D3, which was
      prose-only (a 1501-line doc with a stale `Status:` reached HEAD). See
      DESIGN DOC RULES below.

stdlib only.
"""

import json
import os
import re
import subprocess
import sys

HARNESS_BEGIN = "<!-- PLAN-PARITY-HARNESS:BEGIN -->"
HARNESS_END = "<!-- PLAN-PARITY-HARNESS:END -->"
BANNER_HEADING = "## Current Priority"

REMEDIATION = (
    "RALPH_LOOP guard (H4): the loop does not edit CLAUDE.md, the fix_plan "
    "'## Current Priority' banner, or the AGENT.md PLAN-PARITY-HARNESS "
    "section. A finding that a rule/priority is wrong is an ESCALATION, not an "
    "edit: write an escalation block into the task body (what was found, the "
    "evidence, the proposed wording) and mark the task [!]. Factual status "
    "notes belong in milestone sections or design docs."
)


# Harness mechanism files (M5): the guards, hooks and hook wiring that enforce
# these rules, plus the loop prompt and the reference-cluster / gate-stamp
# plumbing. Whole-file protected, like CLAUDE.md. Repo-relative suffixes.
HARNESS_SUFFIXES = (
    "scripts/ralph_protected_regions.py",
    "scripts/ralph-githooks-test.sh",
    ".claude/settings.json",
    ".codex/hooks.json",
    ".codex/config.toml",
    ".ralph/PROMPT.md",
    ".ralph/gate-exceptions.md",
    "scripts/ref-clusters-ensure.sh",
    "scripts/lib/ref-clusters.sh",
    "scripts/tpch-ref-recover.sh",
    "scripts/lib/gate-stamp.sh",
    ".git/config",
)


def _ends(norm, suffix):
    return norm == suffix or norm.endswith("/" + suffix)


def _home_ralph(norm):
    if norm.startswith("~/.ralph/") or norm == "~/.ralph":
        return True
    home = os.path.expanduser("~")
    if not home or home == "~":
        return False
    try:
        ap = os.path.realpath(norm) if os.path.isabs(norm) else None
    except OSError:
        ap = None
    for cand in (norm, ap):
        if cand and (cand == home + "/.ralph" or cand.startswith(home + "/.ralph/")):
            return True
    return False


def kind_of(path):
    """Return 'claude', 'agent', 'fixplan', 'harness' or None for a path."""
    if not path:
        return None
    norm = path.replace("\\", "/")
    while norm.startswith("./"):
        norm = norm[2:]
    parts = norm.split("/")
    base = parts[-1]
    if base == "CLAUDE.md":
        return "claude"
    if base == "AGENT.md":
        return "agent"
    if _ends(norm, ".ralph/fix_plan.md"):
        return "fixplan"
    if len(parts) >= 2 and parts[-2] == "scripts" and base.startswith("ralph-") and "guard" in base:
        return "harness"
    if ".githooks" in parts[:-1] or "/.git/hooks/" in "/" + norm:
        return "harness"
    if any(_ends(norm, s) for s in HARNESS_SUFFIXES):
        return "harness"
    if _home_ralph(norm):
        return "harness"
    return None


WHOLE_FILE = ("claude", "harness")


def harness_regions(text):
    """All marker-delimited blocks, marker lines included. An unterminated
    BEGIN protects to EOF; a stray END line is itself protected."""
    lines = text.splitlines(keepends=True)
    out, cur = [], None
    for ln in lines:
        s = ln.strip()
        if cur is None:
            if s == HARNESS_BEGIN:
                cur = [ln]
            elif s == HARNESS_END:
                out.append(ln)
        else:
            cur.append(ln)
            if s == HARNESS_END:
                out.append("".join(cur))
                cur = None
    if cur is not None:
        out.append("".join(cur))
    return out


def banner_region(text):
    lines = text.splitlines(keepends=True)
    start = None
    for i, ln in enumerate(lines):
        if start is None:
            if ln.rstrip("\r\n").rstrip() == BANNER_HEADING:
                start = i
        elif ln.startswith("## "):
            return "".join(lines[start:i])
    if start is None:
        return None
    return "".join(lines[start:])


def protected(kind, text):
    if kind == "agent":
        return harness_regions(text)
    if kind == "fixplan":
        return banner_region(text)
    if kind in WHOLE_FILE:
        return text
    return None


def region_changed(kind, before, after):
    if kind in WHOLE_FILE:
        return before != after
    return protected(kind, before) != protected(kind, after)


# ---------------------------------------------------------------- file-guard

def _read(path):
    try:
        with open(path, encoding="utf-8", errors="surrogateescape") as f:
            return f.read()
    except OSError:
        return ""


def _apply_edit(text, old, new, replace_all):
    if old == "":
        return new if text == "" else text + new
    if old not in text:
        return None  # the tool itself will fail; nothing to protect
    if replace_all:
        return text.replace(old, new)
    return text.replace(old, new, 1)


def simulate(tool, ti, current):
    """Return the post-edit file text, or None if not determinable."""
    if tool == "Write":
        return ti.get("content", "")
    if tool == "Edit":
        return _apply_edit(current, ti.get("old_string", ""),
                           ti.get("new_string", ""), bool(ti.get("replace_all")))
    if tool == "MultiEdit":
        text = current
        for e in ti.get("edits") or []:
            nxt = _apply_edit(text, e.get("old_string", ""),
                              e.get("new_string", ""), bool(e.get("replace_all")))
            if nxt is None:
                return None
            text = nxt
        return text
    return None


def deny(reason):
    print(json.dumps({"hookSpecificOutput": {
        "hookEventName": "PreToolUse",
        "permissionDecision": "deny",
        "permissionDecisionReason": reason,
    }}, separators=(",", ":")))


def _what(kind, path):
    if kind == "claude":
        return "CLAUDE.md is owner-only"
    if kind == "harness":
        return path + " is a harness mechanism file (guards/hooks/settings/PROMPT/ref-cluster/gate-stamp plumbing, ~/.ralph) and is owner-only"
    if kind == "agent":
        return "edit changes the AGENT.md PLAN-PARITY-HARNESS section"
    return "edit changes the .ralph/fix_plan.md '## Current Priority' banner"


# Serena MCP tools that write files. Anything else under mcp__serena__ (find_*,
# get_*, read/list/search, memories under .serena/) is not policed here.
SERENA_EDIT = {
    "replace_content", "create_text_file", "replace_symbol_body",
    "insert_after_symbol", "insert_before_symbol", "rename_symbol",
    "safe_delete_symbol", "replace_in_files", "replace_lines", "delete_lines",
    "insert_at_line",
}


def _resolve(path, cwd):
    if not os.path.isabs(path) and not path.startswith("~"):
        path = os.path.join(cwd, path)
    return os.path.normpath(path)


def _serena_files(ti, cwd):
    """Candidate files for replace_in_files (relative_path file/dir + globs)."""
    import fnmatch
    rp = ti.get("relative_path") or ""
    root = _resolve(rp, cwd) if rp else cwd
    if os.path.isfile(root):
        files = [root]
    else:
        files = []
        for dp, dns, fns in os.walk(root):
            dns[:] = [d for d in dns if d not in (".git", "node_modules", "postgres")]
            files.extend(os.path.join(dp, f) for f in fns)
    inc = ti.get("paths_include_glob") or ""
    exc = ti.get("paths_exclude_glob") or ""
    out = []
    for f in files:
        rel = os.path.relpath(f, cwd)
        if inc and not fnmatch.fnmatch(rel, inc):
            continue
        if exc and fnmatch.fnmatch(rel, exc):
            continue
        out.append(f)
    return out


def _serena_guard(name, ti, cwd):
    """Return a deny reason or None."""
    if name == "execute_shell_command":
        guard = os.path.join(os.path.dirname(os.path.abspath(__file__)), "ralph-bash-guard.sh")
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": ti.get("command", "")}})
        r = subprocess.run([guard], input=payload.encode(), capture_output=True)
        out = r.stdout.decode("utf-8", "replace")
        if '"deny"' in out:
            try:
                return json.loads(out)["hookSpecificOutput"]["permissionDecisionReason"]
            except Exception:
                return "RALPH_LOOP guard: serena execute_shell_command denied by ralph-bash-guard.sh"
        return None
    if name not in SERENA_EDIT:
        return None
    if name == "replace_in_files":
        if ti.get("dry_run"):
            return None
        needle = ti.get("needle", "")
        for f in _serena_files(ti, cwd):
            kind = kind_of(f)
            if kind is None:
                continue
            text = _read(f)
            try:
                hit = (needle in text) if ti.get("mode") != "regex" else \
                    re.search(needle, text, re.DOTALL | re.MULTILINE) is not None
            except re.error:
                hit = True
            if hit:
                return REMEDIATION + " (blocked: replace_in_files would touch " + f + ")"
        return None
    rp = ti.get("relative_path") or ""
    path = _resolve(rp, cwd)
    kind = kind_of(path)
    if kind is None:
        return None
    if kind in ("agent", "fixplan"):
        # Region-protected files: simulate the two text-exact tools; every
        # symbol/line-level serena edit is denied outright.
        current = _read(path)
        after = None
        if name == "create_text_file":
            after = ti.get("content", "")
        elif name == "replace_content" and ti.get("mode", "literal") == "literal":
            needle = ti.get("needle", "")
            if needle and needle in current:
                if ti.get("allow_multiple_occurrences"):
                    after = current.replace(needle, ti.get("repl", ""))
                elif current.count(needle) == 1:
                    after = current.replace(needle, ti.get("repl", ""), 1)
                else:
                    return None  # the tool itself errors
            else:
                return None
        else:
            return REMEDIATION + " (blocked: serena " + name + " on " + rp + \
                " cannot be checked against the protected region; use Edit instead)"
        if region_changed(kind, current, after):
            return REMEDIATION + " (blocked: " + _what(kind, rp) + ")"
        return None
    return REMEDIATION + " (blocked: " + _what(kind, rp) + ")"


def file_guard():
    if os.environ.get("RALPH_LOOP") != "1":
        return 0
    try:
        data = json.load(sys.stdin)
    except Exception:
        return 0
    tool = data.get("tool_name", "") or ""
    ti = data.get("tool_input") or {}
    cwd = data.get("cwd") or os.getcwd()
    if tool.startswith("mcp__serena__"):
        reason = _serena_guard(tool[len("mcp__serena__"):], ti, cwd)
        if reason:
            deny(reason)
        return 0
    path = ti.get("file_path") or ti.get("notebook_path") or ""
    if not path:
        return 0
    path = _resolve(path, cwd)
    kind = kind_of(path)
    if kind is None:
        return 0
    if kind in WHOLE_FILE:
        deny(REMEDIATION + " (blocked: " + _what(kind, path) + ")")
        return 0
    current = _read(path)
    after = simulate(tool, ti, current)
    if after is None:
        if tool == "NotebookEdit":
            deny(REMEDIATION + " (blocked: NotebookEdit on a protected file)")
        return 0
    if region_changed(kind, current, after):
        deny(REMEDIATION + " (blocked: " + _what(kind, path) + ")")
    return 0


# ------------------------------------------------------------- check-staged

def _git_show(spec):
    r = subprocess.run(["git", "show", spec], capture_output=True)
    if r.returncode != 0:
        return ""
    return r.stdout.decode("utf-8", errors="surrogateescape")


def check_staged():
    r = subprocess.run(["git", "diff", "--cached", "--name-only", "-z"],
                       capture_output=True, check=True)
    names = [n for n in r.stdout.decode("utf-8", "surrogateescape").split("\0") if n]
    bad = []
    for n in names:
        kind = kind_of(n)
        if kind is None:
            continue
        if kind in WHOLE_FILE:
            bad.append(n + ": " + _what(kind, n))
            continue
        if region_changed(kind, _git_show("HEAD:" + n), _git_show(":" + n)):
            bad.append(n + (": PLAN-PARITY-HARNESS section changed" if kind == "agent"
                            else ": '## Current Priority' banner changed"))
    if bad:
        sys.stderr.write("pre-commit: RALPH_LOOP=1 commit touches protected regions:\n")
        for b in bad:
            sys.stderr.write("  - " + b + "\n")
        sys.stderr.write(REMEDIATION + "\nUnstage those hunks (git restore --staged -p <file>) "
                         "and file an escalation instead.\n")
        return 1
    return 0


# ------------------------------------------------------------- check-ledger

LEDGER = ".ralph/deferral_ledger.md"
OWNER_TOKEN = "OWNER DECISION"


def _ledger_rows(text):
    return [ln.rstrip("\r\n") for ln in text.splitlines() if ln.startswith("|")]


def _row_id(row):
    cells = row.split("|")
    return cells[3].strip() if len(cells) > 3 else ""


def check_ledger_texts(before, after):
    """Deferral ledger is append-only for the loop (M8). Returns error list."""
    from collections import Counter
    errs = []
    if before and not after:
        return [LEDGER + " deleted/emptied (append-only)"]
    b, a = Counter(_ledger_rows(before)), Counter(_ledger_rows(after))
    for row in (b - a).elements():
        errs.append("existing row modified or deleted (append-only): " + row[:160])
    owner_ids = {_row_id(r) for r in b if OWNER_TOKEN in r and _row_id(r)}
    for row in (a - b).elements():
        if OWNER_TOKEN in row:
            errs.append("appended row carries '" + OWNER_TOKEN + "' (owner-only): " + row[:160])
        elif _row_id(row) in owner_ids:
            errs.append("appended row re-uses task-id '" + _row_id(row) +
                        "' of an OWNER DECISION row (the loop may not supersede owner rows): " + row[:160])
    return errs


def check_ledger():
    r = subprocess.run(["git", "diff", "--cached", "--name-only", "--", LEDGER],
                       capture_output=True, check=True)
    if not r.stdout.strip():
        return 0
    errs = check_ledger_texts(_git_show("HEAD:" + LEDGER), _git_show(":" + LEDGER))
    if errs:
        sys.stderr.write("pre-commit: RALPH_LOOP=1 deferral ledger violations:\n")
        for e in errs:
            sys.stderr.write("  - " + e + "\n")
        sys.stderr.write("The ledger is append-only for the loop: add a NEW row with a new task-id; "
                         "owner decisions are written by the owner. Unstage with "
                         "git restore --staged " + LEDGER + ".\n")
        return 1
    return 0


# --------------------------------------------------------- check-designdocs
#
# DESIGN DOC RULES (AGENT.md D3, mechanised). All three fire only for what THIS
# commit stages, so the 20-odd pre-existing over-length docs never block an
# unrelated commit:
#
#   D3.1 size     a staged design doc may not be over DESIGN_MAX_LINES lines
#                 AND longer than it was: "a doc over 800 lines is split before
#                 anything is appended". Shrinking an over-length doc (i.e.
#                 doing the split) is always allowed.
#   D3.2 index    a NEWLY ADDED design doc must be referenced from
#                 docs/design/README.md in the same commit.
#   D3.3 status   when this commit changes a fix_plan task's checkbox state and
#                 a design doc for that task id exists, that doc's `Status:`
#                 line must change in the same commit.
#
# PATH SCHEME: the numbered bucket directories (0000-0049, 0100-0149, ...) are
# a convention that may grow, be renamed, or be absent — docs also live at
# docs/design/<topic>/<file>.md and docs/design/<file>.md. So a design doc is
# ANY *.md under docs/design/ at any depth, EXCEPT the index itself and
# not_ralph/ (the METHODOLOGY3 reading corpus, governed by D6, not D3).

DESIGN_ROOT = "docs/design/"
DESIGN_INDEX = "docs/design/README.md"
DESIGN_EXCLUDED_TOPDIRS = ("not_ralph",)
FIX_PLAN = ".ralph/fix_plan.md"


def design_max_lines():
    try:
        return int(os.environ.get("RALPH_DESIGN_DOC_MAX", "800"))
    except ValueError:
        return 800


def is_design_doc(path):
    norm = path.replace("\\", "/")
    while norm.startswith("./"):
        norm = norm[2:]
    if not norm.startswith(DESIGN_ROOT) or not norm.endswith(".md"):
        return False
    if norm == DESIGN_INDEX:
        return False
    rest = norm[len(DESIGN_ROOT):]
    return rest.split("/")[0] not in DESIGN_EXCLUDED_TOPDIRS


def _nlines(text):
    return len(text.splitlines())


def _status_lines(text):
    return [ln.strip() for ln in text.splitlines()
            if re.match(r"^\s*(\*\*|_|`)?Status\s*:", ln)]


TASK_LINE_RE = re.compile(r"^\s*- \[([ xX!])\] \*\*([^\s*]+)")


def _task_states(text):
    out = {}
    for ln in text.splitlines():
        m = TASK_LINE_RE.match(ln)
        if m:
            out.setdefault(m.group(2).rstrip(".,:;—"), m.group(1).lower())
    return out


def _staged_entries():
    """[(status, path)] for the commit being made."""
    r = subprocess.run(["git", "diff", "--cached", "--name-status", "-z"],
                       capture_output=True, check=True)
    toks = [t for t in r.stdout.decode("utf-8", "surrogateescape").split("\0") if t]
    out, i = [], 0
    while i < len(toks):
        st = toks[i]
        if st[0] in ("R", "C"):          # <status>\0<from>\0<to>
            if i + 2 < len(toks):
                out.append((st[0], toks[i + 2]))
            i += 3
        else:
            if i + 1 < len(toks):
                out.append((st[0], toks[i + 1]))
            i += 2
    return out


def _tracked_design_docs():
    r = subprocess.run(["git", "ls-files", "--", DESIGN_ROOT], capture_output=True)
    if r.returncode != 0:
        return []
    return [n for n in r.stdout.decode("utf-8", "surrogateescape").splitlines()
            if is_design_doc(n)]


def _docs_for_task(tid, tracked, all_ids=()):
    """Design docs named after a task id: <task-id>.md or <task-id>-<slug>.md
    (case-insensitive), whatever directory they sit in — the numbered bucket
    dirs are not part of the match.

    A doc is assigned to its MOST SPECIFIC task: `m0141-s2a-fix1-<slug>.md`
    belongs to M0141-S2a-fix1, not to its parent M0141-S2a, whenever that
    longer id is itself a task in the plan."""
    key = tid.lower()
    longer = [i.lower() for i in all_ids
              if len(i) > len(tid) and i.lower().startswith(key)]
    out = []
    for p in tracked:
        base = p.rsplit("/", 1)[-1][:-3].lower()
        if not (base == key or base.startswith(key + "-")):
            continue
        if any(base == o or base.startswith(o + "-") for o in longer):
            continue
        out.append(p)
    return out


def check_designdocs_errors():
    maxln = design_max_lines()
    errs = []
    entries = _staged_entries()
    staged_docs = [(st, p) for st, p in entries if is_design_doc(p)]

    # D3.1 size / D3.2 index
    index_text = _git_show(":" + DESIGN_INDEX) or _git_show("HEAD:" + DESIGN_INDEX)
    for st, p in staged_docs:
        if st == "D":
            continue
        after = _git_show(":" + p)
        before = _git_show("HEAD:" + p)
        n_after, n_before = _nlines(after), _nlines(before)
        if n_after > maxln and n_after > n_before:
            errs.append(
                "%s is %d lines (D3 limit %d) and this commit makes it longer (%d -> %d). "
                "D3: a doc over %d lines is SPLIT before anything is appended — split it by "
                "task id, link the parts back, and index them in %s in the same commit."
                % (p, n_after, maxln, n_before, n_after, maxln, DESIGN_INDEX))
        if st == "A":
            rel = p[len(DESIGN_ROOT):]
            base = p.rsplit("/", 1)[-1]
            if rel not in index_text and base not in index_text:
                errs.append(
                    "new design doc %s is not referenced from %s. D3: every design doc is "
                    "indexed in the same commit that adds it (add its row/link, then commit "
                    "both together)." % (p, DESIGN_INDEX))

    # D3.3 status — only when this commit moves a fix_plan checkbox.
    if any(p == FIX_PLAN for _s, p in entries):
        before_states = _task_states(_git_show("HEAD:" + FIX_PLAN))
        after_states = _task_states(_git_show(":" + FIX_PLAN))
        moved = [t for t, s in after_states.items()
                 if t in before_states and before_states[t] != s]
        if moved:
            tracked = _tracked_design_docs()
            staged_paths = {p for _s, p in entries}
            for tid in sorted(moved):
                for doc in _docs_for_task(tid, tracked, after_states.keys()):
                    if doc in staged_paths and \
                            _status_lines(_git_show(":" + doc)) != _status_lines(_git_show("HEAD:" + doc)):
                        continue
                    errs.append(
                        "task %s changes state [%s] -> [%s] in %s, but the `Status:` line of its "
                        "design doc %s does not change in this commit. D3: `Status:` is updated "
                        "in the commit that changes the task state."
                        % (tid, before_states[tid], after_states[tid], FIX_PLAN, doc))
    return errs


def check_designdocs():
    errs = check_designdocs_errors()
    if errs:
        sys.stderr.write("pre-commit: RALPH_LOOP=1 design-doc rule (AGENT.md D3) violations:\n")
        for e in errs:
            sys.stderr.write("  - " + e + "\n")
        sys.stderr.write("Set RALPH_DESIGN_DOC_MAX to match AGENT.md if the limit ever moves.\n")
        return 1
    return 0


def main(argv):
    modes = ("file-guard", "check-staged", "check-ledger", "check-designdocs")
    if len(argv) < 2 or argv[1] not in modes:
        sys.stderr.write("usage: ralph_protected_regions.py "
                         "{file-guard|check-staged|check-ledger|check-designdocs}\n")
        return 2
    if argv[1] == "file-guard":
        return file_guard()
    if argv[1] == "check-ledger":
        return check_ledger()
    if argv[1] == "check-designdocs":
        return check_designdocs()
    return check_staged()


if __name__ == "__main__":
    sys.exit(main(sys.argv))
