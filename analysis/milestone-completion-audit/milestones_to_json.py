#!/usr/bin/env python3
"""Convert Ralph's milestone/fix-plan markdown into a hierarchical JSON document.

This is a *preprocessing* step. The downstream goal is to detect work that is
marked DEFERRED / delegated (or only PARTIAL) yet is being managed as if it were
complete. To enable that, we faithfully serialize the source markdown into a
hierarchy that preserves:

  * the structural levels that genuinely exist as nesting in the source
    (milestone -> task -> subtask/slice, via headers + indented checkboxes), and
  * the agent-invented "loop" / "perm" / "Part A/B/C" / date-stamped sub-release
    notions that exist ONLY as prose -- captured as per-node annotation fields
    (status_flags, tags, inline_segments) plus the raw text, NOT as fabricated
    tree levels.

Inputs (under --root):
  .ralph/fix_plan.md                       -> "active"
  completed_milestones/*.md                -> "completed"
  .ralph/deferral_ledger.md                -> "deferral_ledger" (cross-reference)

Output: a single JSON file (default milestones_hierarchy.json at the repo root).

The script is read-only w.r.t. the inputs and writes exactly one output file.
Stdlib only; no third-party packages required.
"""

import argparse
import json
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Dict, List, Optional, Tuple

SCRIPT_DIR = Path(__file__).resolve().parent
# This script lives at <repo>/analysis/milestone-completion-audit/; the repo root
# (where the input markdown lives) is two directory levels up.
DEFAULT_ROOT = str(SCRIPT_DIR.parents[1])

# --------------------------------------------------------------------------- #
# Regexes
# --------------------------------------------------------------------------- #

HEADER_RE = re.compile(r"^(#{1,6})\s+(.*\S)\s*$")
# A header is a milestone iff its text starts with "Milestone N[x]" or "MNNNN[x]".
# A trailing letter (e.g. "Milestone 0028b") is allowed and preserved in the id.
MILESTONE_TEXT_RE = re.compile(
    r"^(?:Milestone\s+(\d+)([a-z]?)|M(\d{3,4})([a-z]?))\b\s*[—–-]?\s*(.*)$"
)

# Bullet list item, optionally a task-list checkbox.
BULLET_RE = re.compile(
    r"^(?P<indent>[ \t]*)[-*]\s+(?:\[(?P<box>[ xX])\]\s+)?(?P<text>.*\S)\s*$"
)

# Task / subtask identifiers, tried in priority order against the first line.
# The bold id need NOT be closed immediately by '**': the dominant style in the
# newer files wraps id + prose in one bold span ("**M0117-0006 — SLRU ...**"),
# so we anchor on the bold-open and close the id on a word boundary.
ID_BOLD_RE = re.compile(r"\*\*\s*(M\d{4}-\d{4}(?:\.\.\d{4})?[a-z]*|M\d{4}-S\d{2}|M\d{4})\b")
ID_SLICE_RE = re.compile(r"^\s*(M\d{4}-S\d{2})\b")
ID_PLAIN_RE = re.compile(r"^\s*(M\d{4}-\d{4}[a-z]*|M\d{4})\b")

DATE_RE = re.compile(r"\b(\d{4}-\d{2}-\d{2})\b")
DESIGN_DOC_RES = [
    re.compile(r"docs/design/(\d{4}-[0-9A-Za-z][0-9A-Za-z-]*)\.md"),
    re.compile(r"\bdesigns?\s+(\d{4}-\d{4})\b"),
    re.compile(r"\(design\s+(\d{4}-\d{4})"),
]

LOOP_RE = re.compile(r"\bLoop-(\d+[a-z]?)\b")
PERM_RE = re.compile(r"(?:\bperms?\s+(\d+(?:[,/]\d+)*)\b|\((\d+)\s+perms?\))")
SLICE_WORD_RE = re.compile(r"\bslice\s+(\d+)\b", re.IGNORECASE)
SLICE_ID_RE = re.compile(r"\b(M\d{4}-S\d{2})\b")
STEP_RE = re.compile(r"\bStep\s+(\d+)\b")

# Inline sub-section markers inside a task body (prose, not nested bullets):
#   **Part B (DEFERRED, ...):**   or   **2026-06-22 read-side half landed (...):**
INLINE_MARKER_RE = re.compile(
    r"(\*\*(?:Part\s+[A-Z]|\d{4}-\d{2}-\d{2})\b[^*]*?\*\*)"
)

# Status keyword detection (case-insensitive). Each flag is a raw signal, not a
# verdict. `complete` is reserved for the authoritative checkbox state; a textual
# "COMPLETE" note is reported separately as `completed_note` so it is not
# conflated with a per-spec "PASS"/"done" mention inside an otherwise-open task.
STATUS_PATTERNS: List[Tuple[str, "re.Pattern[str]"]] = [
    ("partial", re.compile(r"\bpartial\b", re.IGNORECASE)),
    ("deferred", re.compile(
        r"\bdeferred\b(?!-)|\bpostponed\b|\bfuture\s+work\b|"
        r"\bfuture\s+milestone\b|\blater\s+milestone\b|\bout[- ]of[- ]scope\b",
        re.IGNORECASE)),
    ("delegated", re.compile(r"\bdelegated\b", re.IGNORECASE)),
    ("promoted", re.compile(r"\bpromoted\b", re.IGNORECASE)),
    ("blocked", re.compile(r"\bblocked\b", re.IGNORECASE)),
    ("follow_up", re.compile(r"\bfollow-?ups?\b", re.IGNORECASE)),
    ("superseded", re.compile(r"\bsuperseded\b", re.IGNORECASE)),
    ("completed_note", re.compile(r"\bcomplet(?:e|ed|es|ely|ion)\b", re.IGNORECASE)),
]

# --------------------------------------------------------------------------- #
# Helpers
# --------------------------------------------------------------------------- #


def normalize_milestone_num(raw_num: int) -> str:
    """Render a milestone number as the canonical M0000 form."""
    return f"M{int(raw_num):04d}"


def parse_milestone_header(text: str) -> Optional[Tuple[str, int, str]]:
    """If a header's text names a milestone, return (id, num, title)."""
    m = MILESTONE_TEXT_RE.match(text)
    if not m:
        return None
    if m.group(1) is not None:           # "Milestone N[x]"
        num, suffix = int(m.group(1)), m.group(2) or ""
    else:                                # "MNNNN[x]"
        num, suffix = int(m.group(3)), m.group(4) or ""
    title = (m.group(5) or "").strip()
    return normalize_milestone_num(num) + suffix, num, title


def extract_id(first_line: str) -> Optional[str]:
    """Pull a task/subtask identifier out of a bullet's first line."""
    m = ID_BOLD_RE.search(first_line)
    if m:
        return m.group(1)
    m = ID_SLICE_RE.match(first_line)
    if m:
        return m.group(1)
    m = ID_PLAIN_RE.match(first_line)
    if m:
        return m.group(1)
    return None


def id_num_key(id_str: Optional[str]) -> Optional[List[int]]:
    """Numeric sort key derived from an id (e.g. M0118-0008 -> [118, 8])."""
    if not id_str:
        return None
    head = id_str.split("..")[0]          # span "M0117-0001..0005" -> "M0117-0001"
    nums = re.findall(r"\d+", head)
    if not nums:
        return None
    return [int(n) for n in nums]


def clean_title(first_line: str) -> str:
    """Strip checkbox + bold-id markers from a bullet's first line."""
    t = first_line
    t = ID_BOLD_RE.sub("", t, count=1)
    t = t.replace("**", " ")              # drop residual bold markers
    t = re.sub(r"^\s*[—–:-]\s*", "", t)   # leading separator left after id removal
    t = re.sub(r"\s+", " ", t).strip()
    return t


def detect_status_flags(text: str, checkbox: Optional[str]) -> List[str]:
    """Derive a set of status flags from text + checkbox state.

    Flags are raw signals, not a verdict. The downstream detector decides what a
    checked-but-also-deferred node means.
    """
    flags = set()
    for flag, pat in STATUS_PATTERNS:
        if pat.search(text):
            flags.add(flag)
    if checkbox == "checked":
        flags.add("complete")
    return sorted(flags)


def _uniq(seq: List[str]) -> List[str]:
    seen: Dict[str, None] = {}
    for x in seq:
        seen.setdefault(x, None)
    return list(seen.keys())


def extract_dates(text: str) -> List[str]:
    return _uniq(DATE_RE.findall(text))


def extract_design_docs(text: str) -> List[str]:
    out: List[str] = []
    for rx in DESIGN_DOC_RES:
        out.extend(rx.findall(text))
    return _uniq(out)


def extract_tags(text: str) -> Dict[str, List]:
    loops = _uniq([f"Loop-{m}" for m in LOOP_RE.findall(text)])
    perms = _uniq([g1 or g2 for (g1, g2) in PERM_RE.findall(text)])
    slices = _uniq(
        [f"slice {m}" for m in SLICE_WORD_RE.findall(text)]
        + SLICE_ID_RE.findall(text)
    )
    steps = _uniq([f"Step {m}" for m in STEP_RE.findall(text)])
    tags: Dict[str, List] = {}
    if loops:
        tags["loops"] = loops
    if perms:
        tags["perms"] = perms
    if slices:
        tags["slices"] = slices
    if steps:
        tags["steps"] = steps
    return tags


def extract_inline_segments(raw_text: str) -> List[dict]:
    """Split a task body on inline **Part X** / **DATE ...:** markers."""
    parts = INLINE_MARKER_RE.split(raw_text)
    # parts = [preamble, marker1, body1, marker2, body2, ...]
    segments: List[dict] = []
    for i in range(1, len(parts), 2):
        marker = parts[i].strip("* ").strip()
        body = parts[i + 1] if i + 1 < len(parts) else ""
        seg_text = (parts[i] + body).strip()
        segments.append({
            "marker": marker,
            "status_flags": detect_status_flags(seg_text, None),
            "dates": extract_dates(seg_text),
            "text": seg_text,
        })
    return segments


# --------------------------------------------------------------------------- #
# Node model + outline parser
# --------------------------------------------------------------------------- #


@dataclass
class Node:
    kind: str                       # "header" | "bullet"
    depth: int                      # stacking depth
    start_line: int
    source_file: str
    first_line: str
    lines: List[str] = field(default_factory=list)
    children: List["Node"] = field(default_factory=list)
    doc_order: int = 0

    # populated in annotate()
    is_milestone: bool = False
    milestone_id: Optional[str] = None
    milestone_num: Optional[int] = None
    level: str = ""
    node_id: Optional[str] = None
    num_key: Optional[List[int]] = None
    title: str = ""
    checkbox: Optional[str] = None

    @property
    def raw_text(self) -> str:
        return "\n".join(self.lines).strip("\n").rstrip()


def parse_file(path: Path, rel: str, order_counter: List[int]) -> Node:
    """Parse one markdown file into a generic outline tree under a sentinel root.

    Headers nest only under shallower *headers* (they ignore bullets when
    locating a parent); bullets nest by header context + 2-space indentation.
    Non-structural lines (wrapped prose, blanks) accrete onto the current node.
    """
    root = Node(kind="header", depth=0, start_line=0, source_file=rel,
                first_line="", lines=[])
    stack: List[Node] = [root]
    last_header_depth = 0

    def push(node: Node) -> None:
        node.doc_order = order_counter[0]
        order_counter[0] += 1
        stack[-1].children.append(node)
        stack.append(node)

    lines = path.read_text(encoding="utf-8").splitlines()
    for i, line in enumerate(lines, 1):
        hm = HEADER_RE.match(line)
        if hm:
            depth = len(hm.group(1))
            # Headers attach to the nearest shallower header; pop bullets too.
            while len(stack) > 1 and (
                stack[-1].kind == "bullet" or stack[-1].depth >= depth
            ):
                stack.pop()
            node = Node(kind="header", depth=depth, start_line=i,
                        source_file=rel, first_line=hm.group(2),
                        lines=[line])
            push(node)
            last_header_depth = depth
            continue

        bm = BULLET_RE.match(line)
        if bm:
            indent = bm.group("indent").replace("\t", "  ")
            indent_level = len(indent) // 2
            depth = last_header_depth + 1 + indent_level
            while len(stack) > 1 and stack[-1].kind == "bullet" \
                    and stack[-1].depth >= depth:
                stack.pop()
            box = bm.group("box")
            checkbox = None
            if box is not None:
                checkbox = "checked" if box.lower() == "x" else "unchecked"
            node = Node(kind="bullet", depth=depth, start_line=i,
                        source_file=rel, first_line=bm.group("text"),
                        lines=[line])
            node.checkbox = checkbox
            push(node)
            continue

        # Continuation / prose / blank line: accrete onto the current node.
        stack[-1].lines.append(line)

    return root


def annotate(node: Node, in_milestone: bool, is_direct_task: bool) -> None:
    """Fill in id/level/status/tags for a node and recurse, classifying levels."""
    if node.kind == "header":
        # A milestone is specifically an H2 header naming a milestone. H1 is a
        # doc/archive title (e.g. "# M0100-0005 historical ...") and H3 is a
        # sub-section ("### M0092 outcome ...") -- neither is a milestone def.
        ms = parse_milestone_header(node.first_line) if node.depth == 2 else None
        if ms:
            node.is_milestone = True
            node.milestone_id, node.milestone_num, node.title = ms
            node.node_id = node.milestone_id
            node.num_key = [node.milestone_num]
            node.level = "milestone"
        else:
            node.title = node.first_line.strip()
            node.level = "section"
    else:  # bullet
        node.node_id = extract_id(node.first_line)
        node.num_key = id_num_key(node.node_id)
        node.title = clean_title(node.first_line)
        node.level = "task" if is_direct_task else "subtask"

    body = node.raw_text
    node.checkbox = node.checkbox  # already set for bullets
    node._status_flags = detect_status_flags(body, node.checkbox)  # type: ignore[attr-defined]
    node._dates = extract_dates(body)                              # type: ignore[attr-defined]
    node._design_docs = extract_design_docs(body)                 # type: ignore[attr-defined]
    node._tags = extract_tags(body)                               # type: ignore[attr-defined]
    node._inline = (extract_inline_segments(body)                 # type: ignore[attr-defined]
                    if node.level in ("task", "subtask") else [])

    now_in_ms = in_milestone or node.is_milestone
    for child in node.children:
        # A child is a "task" when it's a bullet directly under a milestone.
        child_is_direct_task = (
            child.kind == "bullet" and now_in_ms and node.is_milestone
        )
        annotate(child, now_in_ms, child_is_direct_task)


def sort_key(node: Node):
    nk = node.num_key if node.num_key is not None else [10 ** 9]
    return (nk, node.doc_order)


def sort_tree(node: Node) -> None:
    node.children.sort(key=sort_key)
    for c in node.children:
        sort_tree(c)


def node_to_dict(node: Node) -> dict:
    d = {
        "id": node.node_id,
        "level": node.level,
        "num_key": node.num_key,
        "doc_order": node.doc_order,
        "title": node.title,
        "checkbox": node.checkbox,
        "status_flags": getattr(node, "_status_flags", []),
        "dates": getattr(node, "_dates", []),
        "design_docs": getattr(node, "_design_docs", []),
        "tags": getattr(node, "_tags", {}),
        "raw_text": node.raw_text,
        "source_file": node.source_file,
        "line": node.start_line,
        "inline_segments": getattr(node, "_inline", []),
        "children": [node_to_dict(c) for c in node.children],
    }
    return d


def collect_milestones(root: Node) -> List[Node]:
    """DFS for milestone nodes regardless of where they sit in the tree."""
    out: List[Node] = []

    def walk(n: Node) -> None:
        for c in n.children:
            if c.is_milestone:
                out.append(c)
                # do not descend past a milestone looking for more milestones
                # (milestones are never nested inside milestones here)
            else:
                walk(c)
    walk(root)
    out.sort(key=sort_key)
    return out


def node_to_dict_pruned(node: Node) -> dict:
    """Like node_to_dict but drops milestone subtrees (exported separately)."""
    d = node_to_dict(node)
    d["children"] = [node_to_dict_pruned(c) for c in node.children
                     if not c.is_milestone]
    return d


def collect_sections(root: Node) -> List[dict]:
    """Non-milestone top-level structure (doc title, Notes, Current Priority,
    prose-only loop logs). Milestone subtrees are pruned out so nothing is
    duplicated, but all other content -- including nested bullets -- is kept."""
    return [node_to_dict_pruned(c) for c in root.children if not c.is_milestone]


# --------------------------------------------------------------------------- #
# Deferral ledger
# --------------------------------------------------------------------------- #

def parse_ledger(path: Path, rel: str) -> List[dict]:
    """Parse the 6-column deferral ledger.

    Cells may contain literal '|' (code spans, "A | B"), so a naive split over-
    or under-counts columns. The first two columns (date, task-id) never contain
    a pipe, so they are extracted reliably; the prose remainder is preserved
    verbatim in `rest` (lossless) and split into the four named columns only when
    the row has exactly six cells.
    """
    if not path.exists():
        return []
    rows: List[dict] = []
    for i, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        s = line.strip()
        if not (s.startswith("|") and s.endswith("|")):
            continue
        cells = [c.strip() for c in s.strip("|").split("|")]
        if cells and cells[0].lower() == "date":             # header row
            continue
        if cells and cells[0] and set(cells[0]) <= set("-: "):  # |---| separator
            continue
        row = {
            "date": cells[0] if cells else "",
            "task_id": cells[1] if len(cells) > 1 else "",
            "rest": " | ".join(cells[2:]),
            "line": i,
            "source_file": rel,
            "raw": s,
        }
        if len(cells) == 6:
            row["landed"] = cells[2]
            row["deferred"] = cells[3]
            row["resume_point"] = cells[4]
            row["why"] = cells[5]
        rows.append(row)
    return rows


# --------------------------------------------------------------------------- #
# Build + summary
# --------------------------------------------------------------------------- #


def build_file_section(path: Path, rel: str, order_counter: List[int]) -> dict:
    root = parse_file(path, rel, order_counter)
    for c in root.children:
        annotate(c, in_milestone=False, is_direct_task=False)
    sort_tree(root)
    milestones = [node_to_dict(m) for m in collect_milestones(root)]
    sections = collect_sections(root)
    return {"file": rel, "milestones": milestones, "sections": sections}


def count_nodes(d: dict) -> int:
    return 1 + sum(count_nodes(c) for c in d.get("children", []))


def iter_nodes(d: dict):
    yield d
    for c in d.get("children", []):
        yield from iter_nodes(c)


def find_inconsistencies(milestones: List[dict]) -> List[dict]:
    """Preview of the downstream signal: checked yet deferred/delegated/partial."""
    bad: List[dict] = []
    risk = {"deferred", "delegated", "partial"}
    for ms in milestones:
        for n in iter_nodes(ms):
            flags = set(n.get("status_flags", []))
            seg_flags = set()
            for seg in n.get("inline_segments", []):
                seg_flags |= set(seg.get("status_flags", []))
            checked = n.get("checkbox") == "checked"
            if checked and (flags & risk or seg_flags & risk):
                bad.append(n)
    return bad


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--root", default=DEFAULT_ROOT,
                    help="repository root (default: %(default)s)")
    ap.add_argument("--output", default=None,
                    help="output JSON path (default: <root>/milestones_hierarchy.json)")
    ap.add_argument("--no-ledger", action="store_true",
                    help="skip parsing .ralph/deferral_ledger.md")
    args = ap.parse_args()

    root = Path(args.root).resolve()
    out_path = Path(args.output) if args.output else SCRIPT_DIR / "milestones_hierarchy.json"

    order_counter = [0]
    inputs: List[str] = []

    # active: .ralph/fix_plan.md
    fix_plan = root / ".ralph" / "fix_plan.md"
    if not fix_plan.exists():
        print(f"ERROR: {fix_plan} not found", file=sys.stderr)
        return 2
    print(f"Parsing {fix_plan.relative_to(root)} ...")
    active = build_file_section(fix_plan, str(fix_plan.relative_to(root)), order_counter)
    inputs.append(active["file"])

    # completed: completed_milestones/*.md (sorted by name)
    completed_dir = root / "completed_milestones"
    completed_files: List[dict] = []
    if completed_dir.is_dir():
        for path in sorted(completed_dir.glob("*.md")):
            rel = str(path.relative_to(root))
            print(f"Parsing {rel} ...")
            completed_files.append(build_file_section(path, rel, order_counter))
            inputs.append(rel)
    else:
        print(f"WARNING: {completed_dir} not found; 'completed' will be empty",
              file=sys.stderr)

    # deferral ledger
    ledger: List[dict] = []
    if not args.no_ledger:
        ledger_path = root / ".ralph" / "deferral_ledger.md"
        if ledger_path.exists():
            print(f"Parsing {ledger_path.relative_to(root)} ...")
            ledger = parse_ledger(ledger_path, str(ledger_path.relative_to(root)))
            inputs.append(str(ledger_path.relative_to(root)))

    # totals
    all_milestone_dicts = list(active["milestones"])
    for f in completed_files:
        all_milestone_dicts.extend(f["milestones"])
    node_total = sum(count_nodes(m) for m in all_milestone_dicts)

    document = {
        "meta": {
            "root": str(root),
            "inputs": inputs,
            "milestone_count": len(all_milestone_dicts),
            "node_count": node_total,
            "ledger_rows": len(ledger),
        },
        "active": {
            "source": active["file"],
            "milestones": active["milestones"],
            "sections": active["sections"],
        },
        "completed": {"files": completed_files},
        "deferral_ledger": ledger,
    }

    out_path.write_text(
        json.dumps(document, indent=2, ensure_ascii=False) + "\n",
        encoding="utf-8",
    )

    # ---- summary -------------------------------------------------------- #
    active_tasks = sum(
        sum(1 for n in iter_nodes(m) if n["level"] in ("task", "subtask"))
        for m in active["milestones"]
    )
    print("\n=== summary ===")
    print(f"active milestones:    {len(active['milestones'])}")
    print(f"active task/subtasks: {active_tasks}")
    completed_ms = sum(len(f["milestones"]) for f in completed_files)
    print(f"completed milestones: {completed_ms} across {len(completed_files)} files")
    print(f"ledger rows:          {len(ledger)}")
    print(f"total nodes:          {node_total}")

    incons = find_inconsistencies(all_milestone_dicts)
    print(f"\ncheckbox-checked but deferred/delegated/partial: {len(incons)} node(s)")
    for n in incons[:8]:
        flags = ",".join(n.get("status_flags", []))
        print(f"  - [{n['source_file']}:{n['line']}] {n.get('id') or '(no-id)'}"
              f" [{flags}] {n['title'][:70]}")

    print(f"\nWrote {out_path}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
