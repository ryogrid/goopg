#!/usr/bin/env python3
"""Split resolved rows out of .ralph/deferral_ledger.md.

- Residual (non-'resolved' status) rows stay in .ralph/deferral_ledger.md (single file).
- 'resolved' rows move to completed_defferral/completed_deferral_NNN.md, chunked to
  <= ~350 KB each, each a standalone renderable markdown table (own header+separator).
- Writes completed_defferral/README.md index.

Status = field 2 of the '|'-delimited row, trimmed. Only EXACTLY 'resolved' moves.
Rows copied verbatim (preserve existing entity-escaping). Original order preserved.
"""
import os
import re
import sys

REPO = "/home/ryo/work/goopg/goopg"
LEDGER = os.path.join(REPO, ".ralph/deferral_ledger.md")
OUTDIR = os.path.join(REPO, "completed_defferral")
CHUNK_BYTES = 350_000

with open(LEDGER, "r", encoding="utf-8") as f:
    lines = f.read().split("\n")

# Find separator line (the '| :-- | ... |' row). Header = everything up to & incl. it.
sep_idx = None
for i, ln in enumerate(lines):
    if ln.lstrip().startswith("|") and ":--" in ln:
        sep_idx = i
        break
if sep_idx is None:
    sys.exit("ERROR: could not locate table separator row")

header_block = lines[: sep_idx + 1]          # title + intro + col-header + separator
table_header = lines[sep_idx - 1]            # '| status | date | ... |'
table_sep = lines[sep_idx]                   # '| :-- | ... |'

# The remainder: data rows (start with '|') + any trailing non-row lines.
body = lines[sep_idx + 1:]
data_rows = [ln for ln in body if ln.startswith("|")]
trailing = [ln for ln in body if ln.strip() and not ln.startswith("|")]  # e.g. footer prose

def status_of(row: str) -> str:
    parts = row.split("|")
    # parts[0] == '' (leading pipe); parts[1] is the status cell
    return parts[1].strip() if len(parts) > 1 else ""

resolved, residual = [], []
status_hist = {}
for row in data_rows:
    st = status_of(row)
    status_hist[st] = status_hist.get(st, 0) + 1
    (resolved if st == "resolved" else residual).append(row)

# --- Rewrite residual ledger (single file) ---
new_ledger = "\n".join(header_block + residual + ([""] + trailing if trailing else []))
if not new_ledger.endswith("\n"):
    new_ledger += "\n"
with open(LEDGER, "w", encoding="utf-8") as f:
    f.write(new_ledger)

# --- date range helper (field 3 = date, YYYY-MM-DD) ---
date_re = re.compile(r"\d{4}-\d{2}-\d{2}")
def date_of(row):
    parts = row.split("|")
    if len(parts) > 2:
        m = date_re.search(parts[2])
        if m:
            return m.group(0)
    return None

# --- Chunk resolved rows into standalone renderable tables ---
os.makedirs(OUTDIR, exist_ok=True)
chunks = []  # (filename, rows)
cur, cur_bytes = [], 0
for row in resolved:
    rb = len(row.encode("utf-8")) + 1
    if cur and cur_bytes + rb > CHUNK_BYTES:
        chunks.append(cur)
        cur, cur_bytes = [], 0
    cur.append(row)
    cur_bytes += rb
if cur:
    chunks.append(cur)

index_rows = []
for n, rows in enumerate(chunks, 1):
    fname = f"completed_deferral_{n:03d}.md"
    dates = [d for d in (date_of(r) for r in rows) if d]
    lo, hi = (min(dates), max(dates)) if dates else ("?", "?")
    head = [
        f"# Completed (resolved) Deferrals — Part {n}",
        "",
        (f"Resolved deferral rows archived from `.ralph/deferral_ledger.md` "
         f"(part {n} of {len(chunks)}). See that file for still-open rows. "
         f"Original ledger order preserved."),
        "",
        table_header,
        table_sep,
    ]
    content = "\n".join(head + rows) + "\n"
    with open(os.path.join(OUTDIR, fname), "w", encoding="utf-8") as f:
        f.write(content)
    index_rows.append((fname, len(rows), lo, hi, len(content.encode("utf-8"))))

# --- README index ---
readme = [
    "# Completed Deferral Archive",
    "",
    ("Resolved rows moved out of `.ralph/deferral_ledger.md` to keep the live ledger small "
     "and to stay within GitHub's markdown-render size limit. Each part below is a "
     "standalone renderable table. Open/unresolved rows remain in "
     "`.ralph/deferral_ledger.md`."),
    "",
    "| part | rows | date range | size |",
    "| :-- | --: | :-- | --: |",
]
for fname, nrows, lo, hi, nbytes in index_rows:
    readme.append(f"| [{fname}]({fname}) | {nrows} | {lo} → {hi} | {nbytes//1024} KB |")
readme.append("")
with open(os.path.join(OUTDIR, "README.md"), "w", encoding="utf-8") as f:
    f.write("\n".join(readme) + "\n")

# --- Summary to stdout (so the caller verifies without reading big files) ---
print("=== STATUS HISTOGRAM (data rows) ===")
for st, c in sorted(status_hist.items(), key=lambda kv: -kv[1]):
    print(f"  {c:5d}  {st!r}")
print(f"\ntotal data rows      : {len(data_rows)}")
print(f"  resolved (moved)   : {len(resolved)}")
print(f"  residual (kept)    : {len(residual)}")
print(f"  conservation check : {len(resolved) + len(residual)} == {len(data_rows)} -> "
      f"{'OK' if len(resolved)+len(residual)==len(data_rows) else 'MISMATCH!!'}")
print(f"\nchunks written: {len(chunks)}")
for fname, nrows, lo, hi, nbytes in index_rows:
    flag = " <== OVER 350KB" if nbytes > CHUNK_BYTES + 20000 else ""
    print(f"  {fname}: {nrows} rows, {nbytes//1024} KB, {lo}..{hi}{flag}")
print(f"\nresidual ledger bytes: {len(new_ledger.encode('utf-8'))//1024} KB, "
      f"lines: {new_ledger.count(chr(10))}")
