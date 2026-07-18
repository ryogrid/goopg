# gocomplexity

A Python static-analysis tool that tracks the **health of a Go codebase** by
measuring **cyclomatic** and **cognitive** complexity across its production
sources, then emitting machine- and human-readable reports. It is built for
large codebases such as this from-scratch PostgreSQL reimplementation and, by
default, analyzes production code only (tests and auxiliary tooling excluded).

It wraps two established Go tools — [`gocyclo`](https://github.com/fzipp/gocyclo)
and [`gocognit`](https://github.com/uudashr/gocognit) — and adds file
collection, a statistics engine, and report generation on top.

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
| `--quiet` | Suppress the console summary. |
| `--version` | Print version and exit. |

## Configuration

All keys are optional and fall back to built-in defaults; CLI flags override the
config file. See `config.yaml` for the full example. Defaults (spec §4/§6):

- **Excluded dirs:** `vendor`, `third_party`, `testutil`, `testport`, `tmp`,
  `tools`, `examples`
- **Excluded patterns:** `*_test.go`
- **Thresholds:** `10, 15, 20, 30`

## Metrics

- **Cyclomatic complexity** — independent paths through a function (`gocyclo`).
- **Cognitive complexity** — how hard a function is to *understand*, penalizing
  nesting and breaks in linear flow (`gocognit`).

Reported statistics (per metric): count, mean, median, max, P90/P95/P99, and the
number of functions above each configured threshold, rolled up at the project,
package, directory, and file levels.

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

The following metrics from the design spec (§8) are not yet implemented; they
require a bespoke Go AST / call-graph analyzer rather than an off-the-shelf tool:

- Maintainability Index
- Halstead metrics
- Fan-in / Fan-out
