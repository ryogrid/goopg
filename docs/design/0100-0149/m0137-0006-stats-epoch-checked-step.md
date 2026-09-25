# M0137-0006 — make the stats-epoch declaration a checked step

Status: accepted

## Context

`METHODOLOGY3/03-process-retrospective.md` "Stats-epoch drift" and
`02-open-problems.md` N23 both name the same standing gap: a values sweep
re-samples statistics and opens a new epoch, R120 §6 attributed two apparent
"worsenings" in a flag-OFF/flag-ON A/B to drift rather than the flag, and the
rule that followed — *"re-take the OFF baseline; all A/B numbers same-epoch"*
— was written down and never enforced by any tool. Same-code ANALYZE drift
was separately measured at **1.31x** on Q9 (R128's three committed captures),
wider than some effects the previous phase was claiming to have found.

M0137-0002 landed the **declaration** half: `scripts/lib/capture-stamp.sh`
appends a `# stats-epoch: <fingerprint>` line to every
`scripts/capture-tpch.sh` / `scripts/capture-tpcds.sh` artefact, fingerprinting
`(relname, n_live_tup)` over `pg_stat_user_tables` (a fingerprint, not
`last_analyze`, because goopg's `last_analyze`/`last_autoanalyze` are always
`NULL` — `PGStatTablesRowsForDBOid`, `internal/catalog/catalog.go` — while
`n_live_tup` is real on both engines: PG's live counter and goopg's
ANALYZE-persisted `reltuples`). That task's own line explicitly deferred the
other half here: *"turning the stats-epoch stamp into a checked/enforced step
rather than a passive artefact field."* This task is that other half.

## What "every A/B artefact" means here — the scope this task actually covers

The literal fix_plan line says "every A/B artefact declares its epoch on both
arms." Read narrowly that is unbounded — there are timing-only arm runners
(`scripts/tpch-acceptance-arm.sh`) that never touch a plan or an estimate at
all, for which a stats-epoch stamp is a different, not-yet-asked question
(TPS drift from a stats change is a real effect, but it is not the class N23
or R120 describe, which is specifically an ESTIMATE/PLAN comparison
contaminated by a stats change between the two things being compared). The
class this task closes is: **any two plan/estimate-audit artefacts that are
about to be diffed against each other as an A or a B of the same underlying
statistics.** Concretely, at HEAD, that is:

- `scripts/capture-tpch.sh` / `scripts/capture-tpcds.sh` output — already
  stamped by M0137-0002, but nothing read the field back.
- `cmd/estimate-audit` output — **not previously stamped at all**, despite
  being the canonical TPC-H baseline tool per M0137-0003 (`estimate-audit
  -plan-only`, never `capture-tpch.sh`) and the tool
  `scripts/tpch-estimate-audit-arm.sh`'s two-arm (`PGSHAPED=0`/`PGSHAPED=1`)
  A/B runs behind. Leaving this tool unstamped would have made the whole
  exercise cover only the demoted TPC-H convenience script and TPC-DS — not
  the tool R120's own drift was measured through.

Out of scope, named rather than silently skipped: `scripts/tpch-acceptance-arm.sh`
and any other pure-timing arm runner (a different instrument, a different
question); relocating `capture-tpch.sh`'s query corpus into git tracking
(M0137-0003's own, separate, still-open item).

## Decision — one fingerprint formula, two producers, one checker

**One fingerprint formula.** `cmd/estimate-audit` computes the identical
fingerprint `scripts/lib/capture-stamp.sh` does — `sha256` over
`"relname|n_live_tup\n"` rows from `pg_stat_user_tables`, ordered by
`relname`, first 16 hex characters — rather than inventing a second,
incompatible stamp format. This was verified by hand: hashing
`"nation|25\nregion|5\n"` through both the bash formula
(`sha256sum <<<"${res}" | cut -c1-16`) and the new Go implementation give the
same `cd91584b25f11563`, which is now pinned as a cross-tool regression test
(`TestHashStatsEpochRowsMatchesCaptureStampFormula`,
`cmd/estimate-audit/main_test.go`) — a silent divergence between the two
formulas would otherwise defeat comparability without either side failing
loudly.

`cmd/estimate-audit` (`main.go`):
- `statsEpochLine(f *flags) string` — prepends `# stats-epoch: <value>\n` to
  every report `<label>.txt`, computed from the PRIMARY (`f.host`/`f.port`/
  `f.db`) capture only. A `--ref-port` PG reference is deliberately NOT
  stamped by this line: that comparison is goopg-vs-PG (`pg-plan-parity-diff.py`
  territory, where the two engines' epochs are never expected to be equal,
  different software over a different `pg_stat_user_tables` population), not
  the same-code-different-flag comparison this task addresses.
- Degrades to an explicit `UNKNOWN(reason)` on every failure path
  (`--from-plans` offline replay has no live connection; `sql.Open`/query
  failure) rather than calling `fatal()` — the same reasoning `renderEnum`'s
  own comment gives for a missing enum-trace log: a secondary provenance
  channel must never abort a run that may have just cost a full TPC-H power
  run.
- The hashing itself is split into a pure `hashStatsEpochRows([]statsEpochRow)
  string` so the formula is unit-testable without a live `*sql.DB`.

**One checker.** `scripts/check-stats-epoch.sh <file> <file> [<file> ...]`:
- Extracts each file's first `# stats-epoch: ` line.
- A file with none of that line at all is an **operational** failure (exit 2)
  — it isn't a stamped artefact, not a mismatch.
- Any `UNKNOWN(...)` epoch among the inputs is a **check** failure (exit 1):
  an unknown epoch cannot be asserted equal to anything, so it must not
  silently pass as a match.
- Any disagreement among the (now-known, non-UNKNOWN) epochs is a **check**
  failure (exit 1), printing every file's epoch value and pointing at the
  standing rule this enforces.
- All equal and known → exit 0, printing the shared epoch.

This turns "re-take the OFF baseline after a sweep" from a remembered
convention into something a script can gate on: run it on the pair before
believing a diff, and a mismatched/unknown epoch stops the claim at the door
instead of reaching a report.

## What was NOT done (scope boundary)

- **No wiring into a Makefile target, precommit hook, or CI stage.** Every
  sibling regression test in this milestone
  (`scripts/capture-idempotent-test.py`, `scripts/pg-plan-parity-diff-test.py`)
  is run manually, not from any gate — `check-stats-epoch-test.py` follows the
  same precedent. `check-stats-epoch.sh` itself is meant to be invoked by a
  human (or a future automated arm-runner) as a step in an A/B procedure, the
  same way `scripts/pg-plan-parity-diff.py` is — neither is called from a
  Makefile target either.
- **`scripts/tpch-acceptance-arm.sh` (and other pure-timing arm runners) are
  not stamped** — see the scope section above.
- **No retroactive stamping of pre-M0137-0002 artefacts.** A capture taken
  before this milestone group has no `# stats-epoch:` line at all;
  `check-stats-epoch.sh` reports that as the operational exit-2 case, which
  is the correct answer (the epoch was never recorded, so it genuinely cannot
  be verified), not a defect to fix here.
- **No production planner/executor/catalog code touched.** This is
  documentation-and-instrument work per the M0137 charter; `cmd/estimate-audit`
  is itself a measurement tool, not the engine under test.

## Verification

- `go build ./cmd/estimate-audit/...` — clean.
- `go test ./cmd/estimate-audit/...` — full package green, including four new
  tests: `TestHashStatsEpochRowsMatchesCaptureStampFormula` (pins the
  cross-tool fingerprint to the bash formula's hand-verified value),
  `TestHashStatsEpochRowsOrderSensitive` (a masked ordering bug would hide a
  real stats change), `TestStatsEpochLineOfflineReplayIsUnknown`,
  `TestStatsEpochLineUnreachablePortIsUnknown` (both degrade-to-UNKNOWN paths,
  neither calls `fatal()`).
- `gofmt -l cmd/estimate-audit/main.go cmd/estimate-audit/main_test.go` — clean.
- `python3 scripts/check-stats-epoch-test.py -v` — 8/8 tests pass: matching
  epochs (exit 0), mismatched epochs (exit 1, both values printed), an
  `UNKNOWN` epoch on either side (exit 1, not a silent match), a file missing
  the stamp entirely (exit 2), a missing file (exit 2), the usage guard
  (< 2 files, exit 2), a three-file match and a three-file odd-one-out, and
  an end-to-end run through the REAL `scripts/capture-tpch.sh` (stubbed
  `psql`, no live cluster) verifying the checker against the actual capture
  pipeline's output rather than only a hand-written fixture.
- Manual smoke test of `scripts/check-stats-epoch.sh` against five
  hand-built fixtures (match / mismatch / UNKNOWN / no-stamp / missing-file /
  usage) before writing the automated test, to pin the exact exit codes and
  message shapes the test then asserts.
- **Live end-to-end run against a real throwaway goopg server** (mirroring
  M0137-0003's own probe precedent: private data dir, port 5540, started via
  `scripts/goopg-test-run.sh`, never a shared `:6543x` cluster; schema+data
  from `tpch.DDL()`+`tpch.SampleInserts()` via a throwaway, never-committed
  test file, deleted after the run):
  - Two `estimate-audit -plan-only` runs against the untouched server
    produced the **same real hash** (`0a625970fc214fba`, not `UNKNOWN`), and
    `check-stats-epoch.sh` on the pair reported `MATCH` (exit 0) — the
    identical-statistics case.
  - Doubled `nation`'s row count and ran `ANALYZE nation`, then a third
    `estimate-audit -plan-only` run: the epoch changed to
    `085a4cf59af8c7f9`, and `check-stats-epoch.sh` against the first run
    reported `STATS-EPOCH MISMATCH` (exit 1), printing both hashes and the
    file names — **the exact live defect class this task exists to catch**
    (a values sweep between two captures a human would otherwise diff as if
    they shared a baseline), reproduced against the real engine rather than
    only asserted against hand-written fixtures.
  - Server stopped and all scratch files/binaries removed afterward; nothing
    from this run is committed.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — full run;
  the only failing package is `internal/parser` (a `RangeVar.GroupedJoinUnaliased`
  golden/AST-print drift that fails most of that package's parity tests, not
  only `TestLockingClauseParity` as earlier M0137 task write-ups characterised
  it — confirmed via `git stash` that the identical failure reproduces on
  unmodified HEAD, zero files in `internal/parser` touched by this task).
  Already filed under M-NIGHTLY (`AI-20260914-235643-001`/`-003`).
