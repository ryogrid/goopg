# Mermaid Validator

Validates every Mermaid diagram in the goopg wiki (or any given set of Markdown
files) using a **real headless browser** (Puppeteer + Chromium) running
`mermaid.parse()`. This reproduces GitHub's client-side rendering pipeline, so
it catches the same syntax errors GitHub surfaces (e.g. the "Expecting 'STR',
got 'ALPHA'" failures GitHub reports on `docs/wiki`), which a static linter
cannot detect.

The `docs/wiki` Markdown and Mermaid diagrams are part of the repository and are
rendered by GitHub's web UI; this tool is the check that keeps them green.

## Requirements

- Node.js ≥ 18 (tested with Node 24).
- `npm` (to install dependencies).
- Puppeteer downloads its own Chromium on first install (no system Chrome
  needed). If you already have a system Chrome, see
  [Puppeteer docs](https://pptr.dev/guides/configuration) for how to point
  `PUPPETEER_EXECUTABLE_PATH` at it.

## Setup

```bash
cd tools/mermaid-validator
npm install
```

This installs `mermaid` and `puppeteer` and records the exact versions in
`package-lock.json`. `node_modules/` is gitignored (see the repo root
`.gitignore`).

## Usage

```bash
# Validate the whole wiki (default)
node validate.mjs

# Validate a single file
node validate.mjs ../../docs/wiki/modules/executor.md

# Validate a directory (all .md files recursively)
node validate.mjs ../../docs/wiki/diagrams

# From the repo root, same thing:
node tools/mermaid-validator/validate.mjs docs/wiki
```

Exit code is `0` when every diagram parses cleanly, `1` when any diagram fails.

### Output

On success:

```
Total: 139  Pass: 139  Fail: 0
```

On failure it lists each broken diagram with its file, the line where the
mermaid block starts, and the parser error:

```
Total: 139  Pass: 137  Fail: 2
=== FAILURES ===
docs/wiki/modules/backup.md:180 — Parse error on line 12: ...
docs/wiki/modules/pglz.md:41 — Parse error on line 27: ...
```

## What it checks

- **Extracts** every ` ```mermaid ... ``` ` fenced block from the Markdown.
- **Loads** the diagram text into a headless Chromium page that has loaded the
  same `mermaid` version you installed.
- **Parses** each diagram with `mermaid.parse()` — the exact call GitHub's
  renderer performs.

Because this is a real browser, it is the closest approximation of GitHub's
rendering short of actually uploading the file to GitHub. (GitHub's `gh api
markdown` endpoint only syntax-highlights mermaid code blocks; it does not
parse or validate them.)

## Common mermaid pitfalls caught here

- `note right of` / `note left of` inside a `flowchart`/`stateDiagram` block
  (only valid in `sequenceDiagram`/`classDiagram`).
- Unbalanced or mismatched quote styles in flowchart node labels
  (`["...'`, `'...'`, `""...""`).
- Special characters inside flowchart node labels (`{}`, `[]`, `()`, `|`,
  `<br/>`, commas) that are not double-quoted.
- Reserved tokens in `sequenceDiagram` participant names (e.g. `LINK`).

## How it was used

This tool was built to fix the `docs/wiki` mermaid diagrams on 2026-09-01. The
initial run found **26 of 139** blocks failing; after the fixes all **139/139**
pass. See commit history under `docs/wiki/` for the repair commits.