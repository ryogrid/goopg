# mdtablefix

Detect and repair GitHub-Flavored Markdown (GFM) tables that render wrong on
GitHub — most often because a cell contains an **unescaped pipe** (`|`) that
splits it into extra columns, but also because a cell contains **raw HTML**
or because a **column separator went missing mid-row**.

The key GFM fact this tool is built around: a table row is split into cells
on its pipes **before** the cell contents are parsed as inline Markdown.
That means the *only* way to put a literal pipe in a cell is to escape it as
`\|` — a pipe inside a backtick code span (`` `a|b` ``) is **still** a
column separator on GitHub, contrary to normal inline-code intuition.  So a
cell written `` `if a || b` `` silently renders as several broken columns;
this tool rewrites it to `` `if a \|\| b` `` so it renders as one cell.

## Usage

```bash
# Activate the project venv and run as a module.
venv/bin/python -m tools.mdtablefix --check file.md
venv/bin/python -m tools.mdtablefix --fix file.md
venv/bin/python -m tools.mdtablefix --inplace file.md
venv/bin/python -m tools.mdtablefix --report report.json --check file.md
```

## CLI Reference

| Flag | Description |
|------|-------------|
| `--check` | Report issues to stderr; exit 1 if any table is malformed (default mode). |
| `--fix` | Print the repaired document to stdout; exit 1 if repairs were applied. |
| `--inplace` | Overwrite FILE with the repaired content. |
| `--report PATH` | Write structured diagnostics as JSON to PATH. |

## Algorithm

1. **Parse** the document into blocks: fenced code blocks (```), tables,
   and plain text.  Table-like lines inside code fences are ignored.
   A run of blank lines inside a table body is absorbed — in GFM a blank
   line *terminates* the table, so every row after it renders as literal
   `|`-text.  A blank line followed by a fresh header+separator pair is
   left alone: those are two independent tables and merging them would
   corrupt both.
2. For each table, **tokenize** rows into their intended columns.  The
   author's real column separators are the top-level, whitespace-padded
   `` | `` markers *outside* any backtick span; the tokenizer uses backtick
   spans and a spacing heuristic only to recover this intended structure.
   Two kinds of pipe are recognized as *literal content*, not separators:
   - **In-code pipes** — any `|` inside a `` ` `` … `` ` `` span, e.g. the
     `||` in `` `if a || b` `` or the `|` in `` `pg_dump | psql` ``.
   - **Bare prose pipes** — a `|` (or a `||` run) wedged tightly between
     content characters, e.g. `{ADMIN|INHERIT|SET}` or a C-style `(!X||Y)`.
     A run of pipes flanked by whitespace (`` || ``) is still a genuine
     empty cell.
3. **Repair** malformed rows:
   - *Missing outer pipes*: GFM makes a row's leading/trailing `|` optional,
     so `| a | b | c` is legal.  Such a row is normalised rather than
     rejected — one row missing its trailing pipe must never disqualify the
     other 1000 rows of the table (this is exactly what made the tool report
     "no issues" on a `.ralph/deferral_ledger.md` that rendered broken).
   - *Oversplit* (too many columns): drop **empty** excess cells first —
     `| resolved | | 2026-08-11 | … |` is a doubled separator that shifts
     every following column right, and dropping the empty cell is lossless
     where merging is not.  Remaining excess *content* cells are greedily
     merged right-to-left, escaping the pipe between merged cells as `\|`
     (GFM silently discards cells past the header's column count, so
     merging is what keeps the text visible at all).
   - *Undersplit* (too few columns): two different defects share this
     shape, and they need opposite treatment.  A row the author simply
     ended early is padded with trailing empty cells.  A row whose
     separator was deleted *mid-line* — leaving two columns fused in one
     over-long cell — is reported as an unrepairable `lost_separator`
     and left byte-identical: GFM already pads short rows by itself, so
     appending an empty cell changes nothing about the rendering and only
     silences the report.  The two are told apart by the surrounding rows
     (does some cell exceed its own column's width by a real share of the
     next column's, and is the column that padding would leave empty
     normally populated?).  The split point is not recoverable from the
     text, so a human has to re-insert the `|`.
   - *Unescaped pipes*: every literal `|` left inside a cell — **including
     pipes inside backtick code spans** — is escaped to `\|`, because GFM
     honors only `\|` as a literal pipe in a table.  GitHub strips the
     backslash when rendering, so `` `if a \|\| b` `` displays as
     `if a || b` in a single cell.  This pass is idempotent — already-escaped
     `\|` sequences are left untouched.
   - *Raw HTML*: a `<tag>`-shaped run in a cell is entity-escaped to
     `&lt;tag>`.  A GFM table cell is inline content, so `<db>` in
     `base/<db>/2611` is parsed as raw HTML and **deleted** by GitHub's
     sanitizer — the word vanishes from the rendered cell.  Worse, a
     placeholder that happens to name a real element (`<table>_<col>_check`)
     is *kept*, opening an unbalanced element that nests **every following
     row of the table inside that one cell**.  Backtick spans, markdown
     autolinks (`<https://…>`) and hand-written inline elements (`<br>`,
     `<sub>`, `<kbd>`, …) are left alone.
4. **Format** the repaired table with column-width alignment, preserving
   the original alignment markers (`:---`, `:---:`, `---:`).

## Exit codes

`--check` exits 1 when anything was found.  `--fix` exits 1 when the
document needed repairs.  `--inplace` exits 0 once the file is clean, and
**1 when an unrepairable finding is left** — the file still renders wrong
and a human has to touch it, so it must not read as success.

## Example

```bash
$ venv/bin/python -m tools.mdtablefix --check .ralph/deferral_ledger.md
Line 329, col 6: [missing_cell] Added missing cell at column 6
Line 329, col 7: [missing_cell] Added missing cell at column 7
...
Found 28 issue(s) in .ralph/deferral_ledger.md.
```

## Dependencies

Python 3.12+ standard library only (`argparse`, `json`, `dataclasses`,
`unittest`, `re`).  No third-party packages required.

## Running Tests

```bash
venv/bin/python -m unittest discover -s tools/mdtablefix/tests -v
```
