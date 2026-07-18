# gocomplexity

A Python static-analysis tool that tracks the **health of a Go codebase** by
measuring **complexity, size, maintainability, and duplication** across its
production sources, then emitting machine- and human-readable reports. It is
built for large codebases such as this from-scratch PostgreSQL reimplementation
and, by default, analyzes production code only (**tests and auxiliary tooling
are excluded** — see [Configuration](#configuration)).

It wraps two established Go tools — [`gocyclo`](https://github.com/fzipp/gocyclo)
and [`gocognit`](https://github.com/uudashr/gocognit) — for function-level
complexity, and adds its own dependency-free Go source scanner for lines of
code, the Maintainability Index, and duplicate-code detection, plus a statistics
engine and report generation.

## Project Summary

Every run prints (and embeds in `report.html`) a headline summary:

```
Project Summary
  LOC                        211,016
  Functions                    7,089
  Average CC                    7.45
  Median CC                        3
  P95 CC                          24
  Maximum CC                   1,294
  Average Cognitive             14.2
  Maintainability Index           43
  Duplicate Code               28.6%
```

## Install

1. Install the external Go binaries (once):

   ```bash
   go install github.com/fzipp/gocyclo/cmd/gocyclo@latest
   go install github.com/uudashr/gocognit/cmd/gocognit@latest
   ```

   Ensure `$(go env GOPATH)/bin` is on your `PATH`.

2. Install the Python dependencies into the project venv:

   ```bash
   venv/bin/pip install -r tools/gocomplexity/requirements.txt
   ```

## Usage

Run as a module from the repository root (the venv is assumed active):

```bash
# Analyze the default roots (internal, cmd) and write a timestamped report dir.
venv/bin/python -m tools.gocomplexity

# Analyze explicit roots.
venv/bin/python -m tools.gocomplexity internal/executor internal/planner

# Use a config file and a custom output location.
venv/bin/python -m tools.gocomplexity --config tools/gocomplexity/config.yaml \
    --output-dir analysis/gocomplexity

# CI health gate: fail if any function exceeds cyclomatic complexity 30.
venv/bin/python -m tools.gocomplexity --fail-over 30 --quiet
```

Each run creates a fresh, timestamped output directory:

```
tools/gocomplexity/reports/report_YYYYmmdd_HHMMSS/
    report.html        # self-contained HTML: stats + inline histogram + rankings
    summary.json       # full machine-readable summary
    functions.csv      # every function with its metrics
    packages.csv       # per-package roll-up
    directories.csv    # per-directory roll-up
    files.csv          # per-file roll-up
    histogram.svg      # cyclomatic complexity distribution
```

## CLI reference

| Flag | Description |
|------|-------------|
| `ROOT ...` | Root paths to analyze (default: `internal cmd`, or config `roots`). |
| `--config PATH` | YAML config file (see `config.yaml`). |
| `--output-dir DIR` | Parent dir for the timestamped report dir. |
| `--base DIR` | Repo root the ROOT paths are relative to (default `.`). |
| `--exclude-dir NAME` | Extra directory name to prune (repeatable). |
| `--threshold N` | Complexity threshold to count functions above (repeatable). |
| `--top-functions N` / `--top-packages N` / `--top-files N` | Rows per ranked section. |
| `--fail-over N` | Exit 1 if any function's cyclomatic complexity exceeds `N`. |
| `--dup-min-lines N` | Minimum consecutive code lines for a duplicate block (default 6). |
| `--no-duplication` | Skip duplicate-code detection (the most expensive pass). |
| `--quiet` | Suppress the console summary. |
| `--version` | Print version and exit. |

## Configuration

All keys are optional and fall back to built-in defaults; CLI flags override the
config file. See `config.yaml` for the full example. Defaults (spec §4/§6):

- **Excluded dirs:** `vendor`, `third_party`, `testutil`, `testport`, `tmp`,
  `tools`, `examples`
- **Excluded patterns:** `*_test.go`
- **Thresholds:** `10, 15, 20, 30`
- **Duplication min lines:** `6`

Test code (`*_test.go`) and utility tooling directories are excluded by default,
so all metrics — including LOC, Maintainability Index, and Duplicate Code —
reflect production source only.

## Metrics

Function-level (via the external Go tools):

- **Cyclomatic complexity** — independent paths through a function (`gocyclo`).
- **Cognitive complexity** — how hard a function is to *understand*, penalizing
  nesting and breaks in linear flow (`gocognit`).

Reported statistics (per metric): count, mean, median, max, P90/P95/P99, and the
number of functions above each configured threshold, rolled up at the project,
package, directory, and file levels.

Source-level (via the built-in scanner, `sourcemetrics.py`):

- **LOC** — lines of code: non-blank, non-comment lines. Comments (`//`,
  `/* */`) and string/rune literals are handled by a lightweight Go lexer.
- **Maintainability Index** — the Coleman-Oman index, normalized to 0–100
  (Microsoft variant):
  `MI = max(0, min(100, (171 − 5.2·ln(V) − 0.23·CC − 16.2·ln(LOC)) · 100 / 171))`.
  Because the constants are calibrated for module-sized units, it is evaluated
  from **per-function averages** within each file (mean Halstead volume `V`,
  mean cyclomatic `CC`, mean LOC per function), then rolled up as a
  LOC-weighted average. Rough reading: ≥85 highly maintainable, 65–85 moderate,
  <65 harder to maintain.
- **Halstead volume** — `V = N · log2(n)` over operators/operands, where Go
  keywords and punctuation are operators and identifiers/literals are operands.
  Feeds the Maintainability Index; the project total is reported in
  `summary.json`.
- **Duplicate Code %** — the fraction of code lines inside duplicated blocks. A
  window of `--dup-min-lines` (default 6) consecutive code lines is hashed after
  normalizing literals (numbers → `0`, strings → `"s"`); any window whose
  signature appears at two or more distinct locations marks its lines as
  duplicated. Catches type-1 clones plus literal-normalized (type-2-lite) ones.

> The scanner is intentionally approximate — a hand-written lexer, not a full Go
> AST. It is accurate for line classification and token counting but does not,
> for example, resolve per-function token ranges.

## Determinism

Output is deterministic (spec §7): files are collected in sorted order,
functions are ordered by `(file, line)`, rankings by `(metric desc, key)`, and
percentiles use numpy's default linear interpolation. Only the report directory
name carries a wall-clock timestamp.

## Running tests

```bash
venv/bin/python -m unittest discover -s tools/gocomplexity/tests -v
```

## Future work

The following metric from the design spec (§8) is not yet implemented; it
requires a call-graph analyzer rather than the current line/token scanner:

- Fan-in / Fan-out

(Maintainability Index and Halstead metrics from §8 are now implemented; see
[Metrics](#metrics).)
