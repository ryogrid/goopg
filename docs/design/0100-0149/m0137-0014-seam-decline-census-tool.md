# M0137-0014 — automate the seam-decline census

Status: accepted (landed 2026-09-15)

## Context

`AGENT.md` §"Plan-parity harness"'s "What every M0137–M0143 task report must
contain" makes a **seam-decline census by class, not by total, at a stated
timeout** item 4 of five mandatory report fields — because a class can be
*converted* rather than removed by a fix, and censuses are only comparable at
equal timeouts. Until this task, no tool produced that number: the field
existed only as a written fallback, `grep -oP "seam-decline
reason=\K\S+" <log> | sort | uniq -c`, run by hand against a
`GOOPG_PGSHAPED_DP_TRACE=1` capture.

The 2026-09-15 harness-defect review
(`tmp/METHODLOGY3_RALPH_CHECK0915/03-harness-defects.md`, finding D1) measured
the consequence directly: **3 of the first 22 task reports** carried this
item, and those three only because that loop hand-classified the trace
output — "a number a tool doesn't produce reliably rots in an autonomous
loop" is the review's own conclusion, applied here the same way it was
already applied to category movement (`scripts/pg-plan-parity-diff.py`'s
`CATEGORIES-EXCL-MATCH:` line, landed same-day for item 1). `.ralph/fix_plan.md`
names this task, M0137-0014, as the fix for item 4 specifically.

## What "seam-decline" is

`internal/optimizer/joinsearchtrace.go`'s `traceSeamDecline(reason string,
nrels, nleaves int)` is the trace line emitted whenever
`tryPGShapedJoinSearch` declines to run its DP join-order search on a
statement at all — the search's *other* half from the per-pair `searchTrace`
block: "nothing enumerated" and "the seam declined before enumerating" are
otherwise indistinguishable in the log (M0127-P5.9-r's motivation for the
line). `reason` is a fixed vocabulary — 18 call sites across
`joinsearchseam.go` and `relfromjoinlist.go` (`size-or-no-joinlist`,
`outer-spine`, `leaf-count`, `lateral`, `outer-link-no-sjinfo`, …) — chosen so
"a reader can grep one arm's log for `reason=leaf-count` and get a count"
(the function's own doc comment). This task automates exactly that grep.

## Decision — `scripts/seam-decline-census.py`

A small, dependency-free tool mirroring `scripts/qual-placement-census.py`'s
established shape (a `census()`/`format_report()`/`self_test()` module with a
CLI wrapper, plus a companion `unittest`-based `*-test.py`):

```bash
GOOPG_PGSHAPED_DP_TRACE=1 <capture> 2>trace.log
python3 scripts/seam-decline-census.py --timeout "<label>" trace.log [trace2.log ...]
```

Output:

```
SEAM-DECLINE-CENSUS: timeout=<label> logs=<n> classes=<k> declines=<total>
     <count> reason=<class>
     ...
```

sorted alphabetically by class — the same order `sort | uniq -c` produces
against the manual fallback command, verified byte-for-byte against a real
capture (see Verification). Design choices:

- **`--timeout` is a required, free-text label**, not something parsed out of
  the log (the log carries no timeout of its own — `traceSeamDecline` is
  called from inside the search, which has already run to completion or
  declined by the time anything is logged). Making it required, rather than
  optional-with-a-default, is deliberate: AGENT.md's own wording is
  "censuses are only comparable at equal timeouts", so a report cannot
  produce a census without first stating what it is comparable against. This
  mirrors `check-stats-epoch.sh`'s refusal to compare two epochs silently.
- **Report-only, exit 0** (unlike `qual-placement-census.py`'s fail-loudly
  contract). A census is a number a task report pastes for a human/Ralph-loop
  reader to judge against the previous count — it is not itself a
  pass/fail gate, the same distinction `pg-plan-parity-diff.py` already
  draws for `CATEGORIES:`. Exit 2 is reserved for an operational failure
  (unreadable log, or a missing `--timeout`).
- **Unanchored substring match** (`seam-decline reason=(\S+)` searched
  anywhere in the line, not `^DPTRACE ...$`), so the tool accepts a raw
  capture-tool stderr redirect, a full server log with per-line prefixes
  (timestamp/PID), or a hand-trimmed snippet without requiring the caller to
  strip anything first.
- **Aggregates across multiple log files** in one report (a corpus capture
  commonly spans several `psql`/backend sessions, each its own log) — summed
  per class, with `logs=<n>` in the header so a reader knows how many inputs
  were combined.

## What this task does NOT do

- **Does not run a capture.** The caller still produces the
  `GOOPG_PGSHAPED_DP_TRACE=1` log via whichever capture procedure applies
  (`m0137-0003-baseline-capture-procedure.md` for TPC-H/TPC-DS corpus runs,
  or an ad-hoc single-query probe). This tool's only job is turning that log
  into the report line AGENT.md requires.
- **Does not change the trace vocabulary, the search seam, or any production
  planner code.** Pure tooling, same scope class as M0137-0010's
  `qual-placement-census.py`.
- **Does not wire the tool into any automated gate** (`make` target, `ea-ratchet`,
  pre-commit). It is a report-field producer for a task's own write-up, the
  same standing `qual-placement-census.py`'s self-test-only usage has today —
  no task in the current banner asked for a standing gate on decline-class
  drift, and adding one without a task naming a threshold would be scope
  creep.

## Verification

- `python3 scripts/seam-decline-census.py --self-test` — 4/4 synthetic cases
  pass (empty census, single class, multiple/repeated classes, a line with a
  realistic full-log prefix before `DPTRACE`).
- `python3 scripts/seam-decline-census-test.py -v` — 7/7 `unittest` methods
  pass (self-test delegation, single-log aggregation, multi-log aggregation +
  alphabetical class order, non-decline lines ignored, missing `--timeout`
  exits 2, unreadable log exits 2, zero logs is a usage error).
- **Cross-checked against a real capture, not only synthetic fixtures**:
  `analysis/planner-refactor-take3/c06-q13-diagnosis-20260907/evidence/dppath-on.txt`
  is a committed `GOOPG_PGSHAPED_DP_TRACE=1` log from prior work. The tool's
  output (`declines=1`, `reason=outer-link-no-sjinfo`) matches
  `grep -oP "seam-decline reason=\K\S+" <file> | sort | uniq -c` run
  directly against the same file, byte-for-byte on the count.
- `go build ./...` unaffected (no Go files touched).

## Deferral-ledger and fix-plan bookkeeping

Closes `.ralph/fix_plan.md` M0137-0014. No new ledger row: this task is pure
tooling with no discovered PG-incompatibility or blocked mechanism to record
— the harness-defect gap it closes was tracked as a `[ ]` fix-plan item, not
a ledger row, from the start.
