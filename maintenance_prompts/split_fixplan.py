#!/usr/bin/env python3
"""Archive completed top-level [x] subtasks out of .ralph/fix_plan.md.

Conservative: move ONLY top-level (indent-0 '- [x]') checkbox blocks, INCLUDING their
nested children, up to the next top-level list item / header / '---'. Keep all prose,
all open '- [ ]' blocks, standing rules, and the entire M0123 (active-branch) section.

Outputs:
  - rewrites .ralph/fix_plan.md (kept content; a one-line pointer note where a milestone
    loses completed items)
  - completed_milestones/completed_fix_plan_010.md (archived blocks grouped by milestone)
"""
import os
import re
from collections import OrderedDict

REPO = "/home/ryo/work/goopg/goopg"
FP = os.path.join(REPO, ".ralph/fix_plan.md")
ARCHIVE = os.path.join(REPO, "completed_milestones/completed_fix_plan_010.md")
KEEP_ENTIRE = ("M0123",)  # sections whose done items are NOT archived (active work)
POINTER = "_(completed `[x]` subtasks archived → `completed_milestones/completed_fix_plan_010.md`)_"

TOP_CHECK = re.compile(r"^- \[([ xX])\]")   # top-level checkbox (col 0, '-' marker)
TOP_LIST = re.compile(r"^- ")               # any top-level '- ' bullet (block boundary)
HEADER = re.compile(r"^#{1,6} ")
HR = re.compile(r"^---\s*$")

with open(FP, "r", encoding="utf-8") as f:
    lines = f.read().split("\n")

kept = []
archive = OrderedDict()   # section-header -> [blocks]
cur_section = None
noted = set()
moved_blocks = 0
kept_open_blocks = 0

i = 0
n = len(lines)
while i < n:
    ln = lines[i]
    if HEADER.match(ln):
        if ln.startswith("## "):
            cur_section = ln
        kept.append(ln)
        i += 1
        continue
    m = TOP_CHECK.match(ln)
    if m:
        # gather the full top-level block
        j = i + 1
        while j < n:
            lj = lines[j]
            if TOP_LIST.match(lj) or HEADER.match(lj) or HR.match(lj):
                break
            j += 1
        block = lines[i:j]
        marker = m.group(1)
        is_done = marker in ("x", "X")
        excluded = cur_section is not None and any(k in cur_section for k in KEEP_ENTIRE)
        if is_done and not excluded:
            archive.setdefault(cur_section or "(no section)", []).append(block)
            moved_blocks += 1
            if cur_section not in noted:
                kept.append(POINTER)
                kept.append("")
                noted.add(cur_section)
        else:
            if not is_done:
                kept_open_blocks += 1
            kept.extend(block)
        i = j
        continue
    kept.append(ln)
    i += 1

# collapse runs of 3+ blank lines to a single blank line
collapsed = []
blank = 0
for ln in kept:
    if ln.strip() == "":
        blank += 1
        if blank <= 1:
            collapsed.append(ln)
    else:
        blank = 0
        collapsed.append(ln)

new_fp = "\n".join(collapsed)
if not new_fp.endswith("\n"):
    new_fp += "\n"
with open(FP, "w", encoding="utf-8") as f:
    f.write(new_fp)

# --- build archive file ---
out = [
    "# Completed Fix-Plan Subtasks — Archive 010",
    "",
    ("Completed (`[x]`) top-level subtasks moved out of `.ralph/fix_plan.md` on 2026-07-19 "
     "to keep the live plan small for the Ralph loop. Grouped by milestone, original order "
     "preserved. Open work and standing rules remain in `.ralph/fix_plan.md`; the active "
     "`M0123` section was left intact. Full history is in git."),
    "",
]
for section, blocks in archive.items():
    out.append(section)  # already '## ...'
    out.append("")
    for b in blocks:
        # strip trailing blank lines within a block, then add one separator blank
        bb = list(b)
        while bb and bb[-1].strip() == "":
            bb.pop()
        out.extend(bb)
        out.append("")
os.makedirs(os.path.dirname(ARCHIVE), exist_ok=True)
with open(ARCHIVE, "w", encoding="utf-8") as f:
    f.write("\n".join(out) + "\n")

print(f"moved top-level [x] blocks : {moved_blocks}")
print(f"kept open [ ] blocks       : {kept_open_blocks}")
print(f"sections archived          : {len(archive)}")
for s in archive:
    print(f"   {len(archive[s]):3d}  {s[:70]}")
print(f"\nnew fix_plan.md lines      : {new_fp.count(chr(10))}")
print(f"archive file lines         : {len(out)}")
