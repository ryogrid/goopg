# 0118-0104 — MVCC pruning horizon: temp-vs-permanent prunability (M0118-0009, horizons enabler)

Status: accepted
Spec: `postgres/src/test/isolation/specs/horizons.spec`
Predecessors: 0118-0100 (`->`/`->>`), 0118-0101 (`EXECUTE … INTO STRICT`),
0118-0102 (`Heap Fetches` EXPLAIN infra), 0118-0103 (ordered IOS under
`enable_seqscan = off`).

## What this is

**An enabler, NOT a promotion.** `horizons.spec` exercises the actual MVCC
pruning-horizon contract: pruning and VACUUM must pay attention to concurrent
sessions in the right way. For a **permanent** relation, rows a session deleted
cannot be reclaimed while another session holds an older snapshot; for a
**temporary** relation those same rows ARE reclaimable despite the older
snapshot, because temp data is private to the owning backend
(`GlobalVisTempRels`). The spec reads the heap-fetch count of an ordered
index-only scan as the observable: `Heap Fetches = 2` means two non-killed leaf
entries still need a heap visibility check; `0` means they were reclaimed.

This loop landed the **temporary half** of that contract plus the no-vacuum
permanent permutations — **4 of the 5 permutations now match PG 18.3
byte-for-byte**. The final permutation (permanent-table VACUUM must NOT reclaim
rows an older snapshot still sees) is deferred on an orthogonal latent bug (see
"Deferred" below).

## Expected-output model (the five permutations)

| perm | relation | sequence | post-delete Heap Fetches | landed |
|------|----------|----------|--------------------------|--------|
| 1 | perm | delete (no vacuum) | 2, 2 | ✓ |
| 2 | temp | delete (no vacuum) | 2, **0** | ✓ |
| 3 | temp | delete inside open txn | 2, 2 | ✓ |
| 4 | perm | delete + VACUUM | **2, 2** | ✗ deferred |
| 5 | temp | delete + VACUUM | 0, 0 | ✓ |

The distinguishing mechanic: PG's `kill_prior_tuple` marks index entries
`LP_DEAD` on the first post-delete scan (so the second scan skips them) and heap
pruning physically reclaims the dead tuples. goopg has no `LP_DEAD` index hints;
we reproduce the *observable* with heap pruning + a counting rule (below).

## Changes

### 1. Session-local horizon — `mvcc.Manager.OldestXminForProc(procNum)`

`OldestXmin()` scans every proc-array slot (global). The new
`OldestXminForProc(procNum)` restricts to ONE slot: `min(nextXID, slot.xid,
slot.xmin)` for that backend, falling back to the global `OldestXmin()` when the
slot is out of range or idle (conservative). It still respects the owning
backend's own in-progress transaction (its assigned `xid` and snapshot `xmin`
floor the result), so an uncommitted self-delete is never reclaimable — that is
exactly what keeps permutation 3 (delete inside an open txn) at `Heap Fetches =
2`.

### 2. TEMPORARY relations vacuum/prune at the session-local horizon

- `vacuum.VacuumOptions.Horizon` (new optional field). When > 0 `vacuumCore`
  uses it instead of `mgr.OldestXmin()`.
- `operators_vacuum.go` sets `relOpts.Horizon =
  OldestXminForProc(ctx.Tx.Handle-1)` for a `tbl.Temp` target. Permanent targets
  keep the global horizon. This is what lets permutation 5's temp VACUUM reclaim
  the deleted rows (and `vacuumIndexes` removes their now-stale B-tree entries)
  so the index-only scan returns `Heap Fetches = 0`.

### 3. Index-only-scan prune-on-read + a reclaimed-entry counting rule

In `operators_indexonly.go`:
- **Counting rule (the `kill_prior_tuple` analog).** Before tallying a heap
  fetch on the non-`ALL_VISIBLE` fallback, peek the index entry's root line
  pointer. If it is `LP_UNUSED`/`LP_DEAD` (reclaimed by a prior prune — the
  goopg analog of an entry PG would have killed), it resolves to no heap tuple:
  no `Heap Fetches` tally and no row. This is what drops the count to `0` on a
  re-scan after the temp rows are pruned. For a live or not-yet-pruned tuple the
  pointer is `LP_NORMAL`, so the count is unchanged (permutations 1/2-q3 stay 2).
- **Prune-on-read for temp relations.** After the range scan,
  `pruneTouchedTempPages` reclaims dead tuples on the heap blocks the scan
  fetched, for a `tbl.Temp` relation only, at the session-local horizon. It
  mirrors `vacuumCore`'s reclamation kernel (`storage.PageVacuumPrune` + the
  `LogHeapPruneOpt` WAL hook / `MarkDirty` fallback) but takes no relation lock
  and **deliberately does not set the VM `ALL_VISIBLE` bit** — leaving it clear
  keeps the next scan on the heap-checking fallback path so it skips the
  now-`LP_UNUSED` entries (via the counting rule) instead of trusting the index
  key, which would resurrect the deleted rows. This is what makes permutation 2
  (temp, no VACUUM) drop from `2` to `0` between its two post-delete queries.

Permanent relations are never pruned on read — their reclamation flows through
VACUUM at the global horizon, so permutation 1 keeps `Heap Fetches = 2`.

## Why blast radius is bounded

- `OldestXminForProc` is only consulted by temp-relation vacuum and the temp
  IOS prune-on-read; the global `OldestXmin` path is untouched.
- Prune-on-read fires only for `tbl.Temp` relations (single-session, tiny), only
  on blocks the scan actually fetched, and `PageVacuumPrune` is a no-op when no
  tuple is dead — so live-data scans (TPC-H/pgbench/ordinary queries) take the
  cheap empty-prune path.
- The IOS counting rule changes behaviour only when an index entry's root
  pointer is already `LP_UNUSED`/`LP_DEAD`, which on a non-`ALL_VISIBLE` page
  essentially only happens after this prune-on-read.

## Deferred — permutation 4 (perm-table VACUUM-respects-older-snapshot)

For a permanent table, VACUUM must retain the deleted rows while `lifeline`
holds an older REPEATABLE READ snapshot (`Heap Fetches` stays `2`). goopg's
`OldestXmin()` already pins on any active slot's `xmin`, so the retention is
*one xmin registration away*. The blocker: `lifeline`'s
`ll_start { BEGIN ISOLATION LEVEL REPEATABLE READ; SELECT 1; }` is a single
multi-statement simple-query message. goopg captures an RR transaction's pinned
snapshot lazily at its first *separate-message* statement; here `SELECT 1` shares
the BEGIN's message and the per-message snapshot was taken against the pre-BEGIN
auto-commit tx, so the RR tx never registers its `xmin` — `OldestXmin` ignores
it and VACUUM reclaims the rows (`Heap Fetches = 0`).

The natural fix — capture the RR snapshot at the batched BEGIN's first statement
(what PG does) — was implemented and reverted because it **regresses
`eval-plan-qual-trigger`**: that spec's `s2_b_rr { BEGIN ISOLATION LEVEL
REPEATABLE READ; SELECT 1; }` is the same batched shape, and moving its capture
earlier (to PG-correct timing) exposes a latent goopg gap — goopg fails to raise
`40001 could not serialize access due to concurrent update` for an RR UPDATE of
a row a concurrent transaction updated, when the snapshot is captured at the
correct (earlier) time. goopg's current late-capture happens to compensate. So
permutation 4 is gated on fixing goopg's RR concurrent-update (40001) detection
to be robust to snapshot timing — a separate, deeper change tracked under
M0118-0009. Recorded in the deferral ledger.

## Gates

- `TestPort_IsolationHorizons` (soft `runIsoSpec`) — 4/5 permutations match;
  residual perm 4 isolated by live probe (`L244`/`L254` expected `2` / actual
  `0`).
- `TestOldestXminForProc_SessionLocalIgnoresOtherSnapshots` (new unit).
- `go test -race ./internal/mvcc/... ./internal/vacuum/... ./internal/storage/...`
  PASS; `internal/executor` + `internal/server` PASS.
- Non-regression confirmed: `predicate-hash` and `eval-plan-qual-trigger` PASS
  with the temp-half changes (the dispatch snapshot change that broke them was
  reverted); `vacuum-skip-locked`/`vacuum-concurrent-drop` fail identically on
  clean HEAD (pre-existing timing flakes, unrelated).
- `go build ./...` + `go vet` + `gofmt` clean; pgbench smoke = pre-commit hook.
