# 08-03 — C3 residual: migrate the UPDATE-probe RangeScan to LP_DEAD kill collection

status: design (verified) · date: 2026-07-14 · base: `635cc590` · gates: G-race
(`./internal/access/btree/`, `./internal/executor/`), G-crash, G-waldump,
G-tpch, G-perf → [README](README.md)

> **Verification note (2026-07-14):** this doc was rewritten after a three-pass
> code map (btree kill mechanism · every RangeScan caller · the UPDATE probe)
> corrected the original framing. The kill target is **not** the tuple the
> UPDATE locates and modifies (that tuple is *live* at probe time); it is the
> **dead-pointing entries the probe scan skips**. See §1, §4, and the
> verification appendix (§10). The migration is a faithful mirror of the landed
> read-path collector (`indexScanOp.Next()`), not new machinery.

## 1. Problem and numbers

C3 (btree LP_DEAD on-access cleanup) landed, but kill collection rides the
**read side** — `indexScanOp.Next()` — only. pgbench `-N`'s UPDATE never does a
read index-scan on the pkey; it locates its row via a `RangeScan` **probe**
(`updateOp.updateViaIndex`). Over repeated UPDATEs of the same key, index entries
left pointing at superseded (now-dead) heap versions accumulate. The probe scan
*visits* those dead-pointing entries (its callback is invoked for every entry in
the key range) but **does not collect kills** for them, so they survive until the
no-space purge reclaims them. Evidence:

- 06-01: `pgbench_accounts_pkey` grew +166,830,080 B (one file doubling) in the
  120 s `-N` window; the 600 s soak (`s5c3_soak2_bcfd0ed9`) still showed one
  doubling = 43.6 B/txn, 14.9× below the pre-C3 baseline but not zero.
- 07-01: at scale 500 the pkey doubled again (+832,487,424 B in 120 s), and
  07-02 flags this as **costly under buffer pressure** — a 1.6 GB pkey no longer
  co-resides with the 2.2 GB heap, so dead entries directly raise the miss rate
  (it is no longer just disk waste).

## 2. Current-code map (verified at `635cc590`)

- **`BTree.RangeScan(lo, hi, fn)`** — `internal/access/btree/btree.go:3165` — is
  a thin wrapper over **`RangeScanWithPos(lo, hi, fn(key, ptr, pos ScanPos))`** —
  `btree.go:3161` — which drops the `pos`. Both funnel through `bt.rangeScanPos`
  (`btree.go:3202`). `ScanPos{Blk, Slot, PageLSN}` (`btree.go:3152`) is emitted
  per callback while the leaf is pinned under a **shared** latch; `PageLSN` is
  the D7 re-verify token a later kill pass needs. The scan itself collects **no**
  kills — dead line pointers are simply `continue`-skipped (`btree.go:3244-3248`,
  "C3-S1 — dead entries are invisible to scans").
- **`updateOp.updateViaIndex`** — `internal/executor/operators_storage.go:3741`;
  its `RangeScan` probe is at `:3781`. The callback pins the heap page, calls
  `followHOTChain` (`:3789`) which returns the version visible to *our* snapshot
  via `mvcc.TupleVisible`, and — on success — records only that **live** version
  into a `pending` slice for the modification phase. There are **two** `!found`
  branches: the primary follow (`:3792`) and a re-follow after a foreign
  tuple-lock wait (`:3810`). Both currently `return true, nil` without collecting
  a kill.
- **DELETE has no index probe.** `deleteOp` captures an `idxScan` field but never
  reads it; `deleteOp.Next` always routes through `scanMatching` (the sequential
  matcher). So there is no `deleteViaIndex` and no DELETE RangeScan to migrate.

## 3. PostgreSQL reference

- `src/backend/access/nbtree/nbtutils.c` — `_bt_killitems` marks index tuples
  `LP_DEAD` when a scan finds the heap tuple dead; PG does this for **all** index
  scans, including those backing `ExecUpdate`/`ExecDelete` row location, not just
  read scans. goopg's read-path collector already mirrors this; this doc extends
  it to the UPDATE probe.

## 4. Target design

Give the UPDATE probe the same kill-collection hook `indexScanOp.Next()` uses.
The kill candidate is precisely the entry the probe **skips**: when the probe's
`followHOTChain` returns `found == false`, the entry's whole HOT chain is
invisible to our snapshot. Upgrade that to *dead-to-all* with the same
`OldestXmin`-horizon oracle the read path uses; if it holds, record the entry's
`ScanPos` + heap TID as a kill and let the landed C3 pass mark and reclaim it.

Concretely:
1. Switch `updateViaIndex`'s probe from `RangeScan` to `RangeScanWithPos` so the
   callback receives `pos btree.ScanPos`.
2. In **each** `!found` branch, guarded by `o.ctx.TxnMgr != nil`, evaluate
   `heapChainDeadToAll(slot.Page(), ptr.Offset, o.ctx.TxnMgr.OldestXmin())`
   (`operators_index.go:118` → `storage.TupleDeadToAll`, `prune.go:90`) and, if
   true, append `btree.KillItem{Pos: pos, Ptr: ptr}` to a `killList`.
   **Structural note (verified):** the current probe releases the heap page
   RLock and unpins *before* the `!found` check (`operators_storage.go:3790-3792`
   and `:3808-3810`), because it uses the copying `followHOTChain` (the tuple is
   deep-copied) — unlike the read-path mirror, which holds the RLock across the
   whole branch and uses `followHOTChainNoCopy`. `heapChainDeadToAll` reads
   `slot.Page()` directly, so the implementation must **relocate the
   `RUnlock`+`Unpin` to after the kill check** (keep the page pinned + read-locked
   until the oracle has read it); a naïve "insert into the `!found` branch as
   written" would read an already-unpinned (possibly evicted/reused) page.
3. After the scan drains, flush `tree.KillItems(killList)`
   (`lpdead_kill.go:31`) — the single-leaf exclusive-latched pass that
   re-verifies page-LSN equality and marks `LP_DEAD` via `MarkDirtyHint`.

### Decision log

- **D1 — reuse the landed C3 kill mechanism, do not add a new one.** The
  oracle (`heapChainDeadToAll`), the kill unit (`btree.KillItem`), and the
  marking/purge pass (`bt.KillItems`) all exist; only the *collection* call site
  is missing on the probe path. This is a call-site migration.
- **D2 — scope S1 to the UPDATE probe only.** It is the sole probe whose skipped
  entries are the measured pkey-doubling driver, and DELETE has no probe (§2).
- **D3 — kills respect the same visibility rule C3 established.** Only entries
  whose entire HOT chain is dead below `OldestXmin` are marked — the exact subset
  VACUUM reclaims. `TestLPDeadInvisibleToSearchAndRangeScan` is the guard.
- **D4 — collect in both `!found` branches, for consistency.** The primary
  follow and the post-lock-wait re-follow both address the *same* index entry
  (`ptr`/`pos`). The measured residual is driven entirely by the **primary**
  branch. The second branch's kills will in practice almost always be **dropped**
  by `KillItems`' D7 page-LSN re-verify (`lpdead_kill.go:62`): the tuple-lock
  wait released the leaf latch, so a concurrent writer likely bumped the leaf's
  pd_lsn between `pos` capture and flush. Collecting there is therefore near-zero
  value but harmless (a dropped hint, never corruption) — included only so the
  two branches read identically and a reviewer needn't ask why one kills and the
  other doesn't.

## 5. Invariants and failure modes

- **I1 — never kill a live entry.** Collection uses the all-snapshots-dead
  predicate against `OldestXmin`; a version visible to *any* snapshot (including
  ours) fails it. `TestLPDeadInvisibleTo…` + a new probe-specific test guard this.
- **F1 — never kill the update target or the new version (doubly safe).** The
  update target is `found == true` (visible to us) and is recorded into
  `pending`, never reaching a `!found` branch. The *new* index entry is inserted
  only in the modification phase, **after** the RangeScan has fully drained
  (`maintainUniqueIndexesForInsert`, `operators_storage.go` Phase 2), so the scan
  never observes it; even hypothetically it would be `found == true`. The
  two-phase collect-then-modify structure is what makes this hold.
- **F2 — WAL/standby (O-C3M-2).** `KillItems` marks via `Pool.MarkDirtyHint`,
  which emits **no** WAL and no pd_lsn bump — a purely local hint, exactly as
  the read-path kills and PG's `MarkBufferDirtyHint`. The physical *purge*
  (kept-items record) is separately WAL-logged. The probe-path kills inherit this
  identical local-hint / logged-purge split; no new replication concern.
- **F3 — kill under a writing transaction.** The probe runs inside the updating
  txn, but `OldestXmin` already excludes anything visible to our own snapshot, so
  the oracle can only admit entries whose chain is dead to everyone. Same
  reasoning as the read-path collector, which also runs in arbitrary txns.

### Non-goals (S1)

- **No SSI hook.** The read path's `!found` branch also registers an SSI
  invisible-tuple conflict-out; that is separate phantom-detection machinery the
  UPDATE write path handles elsewhere. S1 adds *only* kill collection, to avoid
  perturbing write-path serialization semantics.
- **No change to the `found == true` update path.**

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| S1 | UPDATE-probe kills | `RangeScan`→`RangeScanWithPos` in `updateViaIndex`; collect a kill in each `!found` branch via `heapChainDeadToAll` under `OldestXmin`; flush `tree.KillItems` after the scan. Add a targeted probe-kill test. | G-race, G-crash, G-tpch |
| S2 | *deferred* — other callers | The 9 non-index-scan RangeScan callers are constraint/conflict probes (upsert arbiter, unique/exclusion checks, deferred rechecks) that act on **live** tuples only (§10); `indexOnlyScanOp` is a clean read-path follow-on but not part of the measured residual. Deferred with a ledger line. | — |
| S3 | perf acceptance | 600 s soak at scale 100 + a scale-500 `-N`: pkey growth per txn should approach 0; the scale-500 miss rate should improve. | G-perf |

## 7. Test-impact matrix

| test | file | slice |
|---|---|---|
| LP_DEAD visibility tolerance | `internal/access/btree/lpdead_tolerance_test.go` | S1 (guard) |
| kill marking / purge | `internal/access/btree/lpdead_kill_test.go` | S1 (guard) |
| RangeScan correctness | `internal/access/btree/btree_test.go` (`TestRangeScan`) | S1 |
| **new** UPDATE-probe kill collection | `internal/executor/` (probe-kill test) | S1 |
| crash recovery | `Crash\|Recovery\|Durability` + `TestKillKillRecovery` | S1 |
| TPC-H spotcheck (Q12/Q13 canonical) | `scripts/tpch-spotcheck.sh` | S1 |

## 8. Performance verification

600 s `-N` soak at scale 100 (the metric C3 exists for): the pkey file should not
double after warm-up; per-txn growth → ~0 (from 43.6 B/txn). Scale-500 `-N`
re-run: the +832 MB/120 s doubling should not recur, and the miss rate (07's
`pinSlow` reload wait) should drop. Record the run dir + commit hash in
`analysis/`. The growth-flattening is the acceptance signal (doc §1 evidence).

## 9. Open questions

- **O-C3M-1 (resolved).** The set of RangeScan callers is enumerated in §10.
  In-scope for kills = the UPDATE probe (`#8`). Optional follow-on =
  `indexOnlyScanOp` (`#2`, same read semantics as the landed `indexScanOp`).
  Out-of-scope = the six constraint/conflict probes (`#3-7,9,10`) and the deferred
  rechecks — they deliberately act on **live** tuples and use a different
  (`isLiveForUniqueCheck`) predicate, so they are not natural kill sites.
- **O-C3M-2 (settled, see F2).** Probe-path LP_DEAD hints are local (not
  replicated), inheriting the original C3 O-C3-2 split; the logged purge is
  unchanged.

## 10. Verification appendix — the RangeScan caller map (verified at HEAD)

Ten production `RangeScan`/`RangeScanWithPos` call sites; exactly one uses the
`WithPos` form (the read scan). "Dead at probe?" is whether the located entry can
be a dead-to-all kill candidate.

| # | file:line | enclosing fn | purpose | kill candidate? |
|---|---|---|---|---|
| 1 | `operators_index.go:441` | `indexScanOp` (WithPos) | SELECT / NLI read scan | **yes — already wired** (`:515-527`) |
| 2 | `operators_indexonly.go:263` | `indexOnlyScanOp.Open` | index-only scan | yes (optional follow-on) |
| 3 | `operators_upsert.go:673` | `probeArbiterByKey` | ON CONFLICT arbiter probe | no — live only |
| 4 | `operators_upsert.go:866` | `findInProgressConflictKey` | ON CONFLICT in-flight probe | no — live only |
| 5 | `operators_upsert.go:1371` | `probeSpeculativeConflict` | ON CONFLICT speculative probe | no — live only |
| 6 | `deferred_unique.go:239` | `recheckDeferredUniqueKey` | deferred UNIQUE recheck | no — live only |
| 7 | `deferred_exclusion.go:193` | `recheckDeferredExclusionEq` | deferred EXCLUSION recheck | no — live only |
| 8 | `operators_storage.go:3781` | `updateViaIndex` | **UPDATE probe** | **yes — S1 target** |
| 9 | `operators_storage.go:7364` | `exclusionCheckOnce` | EXCLUSION check | no — live only |
| 10 | `operators_storage.go:7432` | `uniqueCheckWithWait` | UNIQUE check | no — live only |

FK checks do **not** use RangeScan (they pin heap pages directly, `operators_fk.go`);
MERGE reuses the update/delete/insert machinery and issues no RangeScan of its own.

The reusable mechanism (all verified present at HEAD):
`RangeScanWithPos` / `ScanPos` (`btree.go:3161`/`:3152`) → oracle
`heapChainDeadToAll` (`operators_index.go:118`) → `storage.TupleDeadToAll`
(`prune.go:90`) under `OldestXmin` (`mvcc/manager.go:655`) → unit
`btree.KillItem` (`lpdead_kill.go:10`) → marking/purge `bt.KillItems`
(`lpdead_kill.go:31`), best-effort, page-LSN re-verified, `MarkDirtyHint`
(unlogged local hint). The exact `!found` collection pattern to mirror is
`operators_index.go:515-527`.
