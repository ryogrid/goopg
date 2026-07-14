# root-0028 — amcheck real-tree stress test: leftover debug instrumentation caused nightly race-lane timeouts

## Context

The previous M-NIGHTLY triage loop (`root-0027`) fixed the nightly classifier's
per-package resource-kill misclassification and, in doing so, surfaced two
**never-before-seen** race-lane failures it had been silently swallowing into
a single whole-stage "resource kill" notice: `race/internal/access/btree` and
`race/internal/amcheck` (deferral ledger, task-id `M-NIGHTLY (run
20260715-010036 triage)`). Both hit Go's own `-timeout` SIGQUIT with an
ambiguous signature (no `signal: killed` in their own block) — worth
investigating given `internal/access/btree`'s history of real concurrency
bugs (e.g. M0110-0007's split prev-link race).

This loop reproduced both failures directly and traced them to a single
cause: a resolved investigation's temporary debug instrumentation that was
never cleaned up, quietly serializing a heavy concurrency stress test.

## Reproduction

Both packages pass cleanly standalone, with plenty of margin below the
nightly's 45-minute race-lane budget:

```
go test -race -timeout 15m ./internal/access/btree/   # ok, 23.1s
go test -race -timeout 15m ./internal/amcheck/         # ok, 148.0s
```

Reproducing required the exact nightly cgroup + concurrency configuration
(`ci/batch/stages/stage-race.sh`):

```
GOOPG_CG_UNIT=<unique> GOOPG_MEM_HIGH=6G GOOPG_MEM_MAX=8G GOOPG_MEM_SWAP_MAX=0 \
GOMEMLIMIT=5GiB scripts/goopg-test-run.sh env GOFLAGS=-p=4 \
go test -race -timeout 12m ./internal/access/btree/ ./internal/amcheck/
```

Under this configuration `internal/amcheck` hit its own `-timeout` SIGQUIT
inside `TestVerifyBtreeEngineSilentOnRealConcurrentContended` every single
time — even at a 25-minute per-test budget (`go test -race -timeout 25m -run
TestVerifyBtreeEngineSilentOnRealConcurrentContended ./internal/amcheck/`),
the test never finished (exceeded 1800s). `internal/access/btree` passed
cleanly (23.8s) once `internal/amcheck` was fixed and stopped monopolizing
the shared, memory-capped 4-package co-load — its earlier nightly failure was
pure collateral CPU starvation from sharing the box with amcheck's runaway
test, not a bug of its own.

## Root cause

`TestVerifyBtreeEngineSilentOnRealConcurrentContended`
(`internal/amcheck/verify_nbtree_realtree_test.go`) drives 200,000 inserts
across 64 concurrent writer goroutines into a deliberately tiny 64-slot
buffer pool (to force heavy eviction churn) — a genuinely CPU/lock-contention
heavy test even under normal conditions (172.65s under `-race` standalone).

Its helper, `buildRealTreeConcurrent`, still had **all six** of the
`BTree`-level debug-tracing flags from the M-NIGHTLY `AI-20260708-064334-001`
investigation permanently enabled:

- `bt.DebugTraceInserts`
- `bt.DebugVerifyFastPathInserts`
- `bt.DebugTraceFlushes` (+ `pool.OnFlushSnapshot`)
- `bt.DebugTraceReloads` (+ `pool.OnBlockReload`)
- `bt.DebugTraceContentMu`
- `bt.DebugTraceBufmap` (+ `pool.OnBufmapInsert`/`OnBufmapDelete`)

plus two pool-level flags from the same investigation:

- `pool.DebugValidateCleanEvictions`
- `pool.DebugTraceSlotEvents`

That investigation is long resolved: the root cause was
`storage.bufmap.Insert` stopping at the first tombstone in its open-addressing
probe chain instead of scanning to a true-empty terminator, with permanent
regression coverage in `TestBufmapInsertSkipsPastTombstoneToExistingKey`
(`internal/storage/bufmap_test.go`). `bufpool.go`'s own comment for
`DebugTraceSlotEvents` even says to remove it "alongside
`DebugValidateCleanEvictions` once the root cause is fixed" — but nobody had.

Each flag funnels every pin/unpin/insert from all 64 concurrently racing
goroutines through a handful of shared, mutex-guarded logs — e.g.
`recordContentMuLock`/`recordContentMuUnlock` decode the full page via
`pageItems()` on every `unpinW` and serialize a map/slice append behind one
shared `bt.insertLogMu`. Harmless standalone (a few seconds of extra work
against a 172s baseline), but combined with `-race`'s own heavy
synchronization-primitive overhead and the nightly race lane's
memory-capped, 4-package-concurrent co-load, this serialized the test badly
enough to blow past any reasonable per-package timeout.

## Fix

Removed all 8 flags/hooks from `buildRealTreeConcurrent`
(`internal/amcheck/verify_nbtree_realtree_test.go`), plus the
`bt.FastPathViolations()` diagnostic-log block later in the same test — with
`DebugVerifyFastPathInserts` off, that block was permanently dead, always
logging a misleading "none recorded" as if verification had actually run.

The test's real correctness gate — amcheck's post-hoc structural
verification plus the on-disk leaf-entry diff against every successfully
inserted `(key, TID)` pair — is untouched.

## Results

| | before | after |
|---|---|---|
| `TestVerifyBtreeEngineSilentOnRealConcurrentContended` standalone | 172.65s | 7.05s (24x) |
| full `internal/amcheck` package standalone | 148.0s | 11.4s (13x) |
| both packages under nightly cgroup + `-p=4` co-load | `amcheck` hangs past 25m; `btree` collateral-starved | `btree` 23.8s, `amcheck` 11.8s — both clean |

## Deferred

The now-unused `Debug*`/`Record*` instrumentation itself (`BTree.DebugTrace*`
fields and their methods in `internal/access/btree/btree.go`,
`Pool.DebugValidateCleanEvictions`/`DebugTraceSlotEvents` in
`internal/storage/bufpool.go`) was left in place — it is zero-cost when
unset, and this matches the codebase's existing pattern for other resolved
M-NIGHTLY investigations (e.g. flush/reload tracing left in `btree.go` from
earlier loops). A grep confirmed no other test file uses these flags. A
follow-up loop could delete this dead-code surface entirely if it's judged
worth the risk of touching two production files for a pure cleanup; see the
deferral ledger.

`internal/initdb`/`internal/mvcc`'s still-open ambiguous-SIGQUIT `units`-lane
timeouts (tracked separately since `root-0027`) were not investigated this
loop.

## Follow-up (2026-07-15): `internal/initdb`/`internal/mvcc` confirmed resolved as collateral resource contention

Resume point from the section above. Re-ran the exact nightly `units`-lane
repro technique (`ci/batch/stages/stage-units.sh`'s own command, all 44
non-excluded packages, identical cgroup config) now that this loop's `amcheck`
fix (above) is in place:

```
GOOPG_CG_UNIT=<unique> GOOPG_MEM_HIGH=6G GOOPG_MEM_MAX=8G GOOPG_MEM_SWAP_MAX=0 \
GOMEMLIMIT=5GiB scripts/goopg-test-run.sh env GOFLAGS=-p=4 \
go test -timeout 30m <all 44 units-lane packages>
```

Result: **clean pass**, no timeout, no `signal: killed`, no SIGQUIT dump —
`internal/initdb` 237.79s, `internal/mvcc` 1.30s, 0 `FAIL` across the whole
run. `internal/amcheck` also runs in the (non-`-race`) `units` lane alongside
`initdb`/`mvcc` as one of the same 44 concurrent packages; before this loop's
fix it was the same debug-tracing-bloated
`TestVerifyBtreeEngineSilentOnRealConcurrentContended` (172s→7s standalone)
eating a disproportionate share of the shared 6G/8G memory-capped, `-p=4`
co-load window that `initdb`/`internal/mvcc` were both running inside. With
that hog removed, the full 44-package concurrent run now finishes with
comfortable margin under the nightly's 30-minute budget for every package,
confirming — rather than merely hypothesizing — that `initdb`/`mvcc`'s
`AI-20260715-010036-001`/`-002` nightly timeouts were the SAME collateral
resource-starvation class as `cmd/goopg`/`amcheck`'s already-classified
resource kills, not an independent product hang. No further product code
change needed; this closes the last open item from the `20260715-010036`
nightly triage thread. Deferral ledger: the still-open row is flipped to
`resolved`.
