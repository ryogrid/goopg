# M0144-0010 — ledger bulk-triage tooling

Status: impl landed 2026-09-20 (loop #42); tool + cadence target + first
report committed. Touches `scripts/` + `Makefile` only — no production
code.

Source: `METHODOLOGY4/03-forward-plan.md` §6 — "~2,100 open ledger rows
cannot be triaged row-by-row inside tasks… (1) auto-detects rows whose
referenced code/tests no longer exist, (2) folds same-mechanism rows into
cluster rows, (3) emits only the survivors." Task entry: `.ralph/fix_plan.md`
M0144-0010. This is the M0119-successor cadence mechanism.

## Tool

`scripts/ledger-triage.py` (+ `make ledger-triage`; `LEDGER_TRIAGE_FAST=1`
skips git attribution ~3 s vs ~2.5 min; `LEDGER_TRIAGE_OUT=<file>`,
`LEDGER_TRIAGE_FULL=1` for the full survivor list). **Read-only**: the
ledger is append-only (R7 + `ralph_protected_regions.py` enforcement), so
the tool emits a report, never edits rows.

Pipeline:

1. **Parse** the 7-column table (2,296 rows; stray-pipe cells merge into
   `landed`; open = status `-`/`open`/`[!]` → 2,129 rows).
2. **Extract references** from `landed`/`deferred`/`resume point`/`why`:
   file paths (`*.go` etc., quoted or bare, `:line` tolerated), backtick
   symbols (SQL keywords / GUCs / error codes filtered), `Test*` names.
   `.c`/`.h`/`.y`/`.l` and symbols found only under `postgres/src` are
   **oracle citations** — a port target still exists, so they never vote
   for staleness (a pg ref gone even upstream, e.g. `pg_hba.c`→`hba.c`,
   is reported as annotation only).
3. **Existence**: basename→paths index over `git ls-files` + a
   `postgres/` walk; symbol membership via one-pass identifier sweeps of
   `internal/`+`cmd/` and `postgres/src` (no per-symbol greps).
   Glued `sym/file.go` refs fall back to basename resolution.
4. **Attribution** for gone refs: `git log -1 -- '**/base'` for files,
   `git log -S` for symbols.
5. **Classify**: `stale-candidate` (every resolvable ref gone; ≥1 strong
   ref or ≥3 weak refs), `partial-stale`, `live`, `unverifiable` (no
   extractable ref — prose-only rows survive by default; absence of a
   citation is not staleness).
6. **Cluster**: each row joins the mechanism area of its most-referenced
   code file (`.go`/`.sh`/`.py`/`.sql` — doc citations like `fix_plan.md`
   are bookkeeping, not mechanism areas); ref-less rows group by task-id
   family. Transitive union-find was tried and rejected: shared hubs
   chained 425 rows into one meaningless component.

## First report (HEAD `f2782479e`, ledger @ 2,296 rows)

```
open: 2,129  stale-candidate: 28  partial-stale: 58  live: 593  unverifiable: 1,450
```

- **28 stale-candidates**, each with last-touch attribution — e.g.
  `cast_ddl_recovery.go`/`collation_ddl_recovery.go`/`aggregate_ddl_recovery.go`
  retired by the 2026-07-17 catalog heap-journaling conversions;
  `canonical.go` + `initdb/native_only_audit_test.go` by `1f0a3eca9`
  (WAL dual-emit removal); `scripts/tpcds-sf05-regression.sh` by
  `e2a50de40` (SF0.5→SF0.25 migration); `join_agg.go` "no history" (two
  M-NIGHTLY rows cite a file that never existed).
- Largest survivor clusters: `family:M0134` 503, `family:M0119` 177,
  `family:M0127` 169, then mechanism areas `internal/executor/expr.go` 33,
  `internal/optimizer/planner.go` 32, `internal/initdb/open.go` 23,
  `joinsearchseam.go` 18.
- The 1,450 `unverifiable` rows are prose-only deferrals (e.g.
  "clog-dependent HOT checks") — no machine-checkable citation. They stay
  in the survivor pool for human per-task triage; the tool's contract is
  that `stale-candidate` is *high-precision* (auto-flag), not exhaustive.

Report: `analysis/m0144/m0144-0010-ledger-triage.md` (regenerate:
`LEDGER_TRIAGE_OUT=analysis/m0144/m0144-0010-ledger-triage.md make
ledger-triage`).

## Cadence

Run at the milestone-boundary cadence M0144-0008 pins for SF1 captures —
i.e., when a milestone's last task closes, and before any stocktake. The
28 stale-candidates are the input for an M0119-style resolution pass
(mark `resolved` where the deferred scope is confirmed landed/obsoleted);
survivor clusters are the per-task triage input §6 asks for.

## D2 report fields

1. `CATEGORIES:`/`MATCH`: N/A — harness tooling, no parity capture.
2. shape-delta: N/A. 3. stats epoch: N/A.
4. seam-decline census: N/A — no production change.
5. planning route: unchanged.
6. `Movement: none` — tooling; no plan movement. `Parent: none`.
7. wall times: N/A.
