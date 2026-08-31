# root-0033 — Redo of a redirect-only page prune must compact the page

**Status:** accepted
**Date:** 2026-07-28
**Milestone:** M-NIGHTLY (continuation of [root-0032 §5](root-0032-crash-restart-wal-stream-anchoring.md))
**Area:** `internal/wal` (redo), `internal/storage` (page pruning)

## 1. Symptom

After [root-0032](root-0032-crash-restart-wal-stream-anchoring.md) fixed the WAL
*read* path, the same reproduction —
`analysis/wal-crash-restart-repro.sh` with `LOADSEC=200 KILLAT=170`
(`pgbench -c 16 -j 4` against a fresh cluster, `kill -9` mid-load, restart) —
still left the cluster **unstartable**, now failing one stage later, inside redo:

```
goopg start: goopg: wal replay: wal: replay record 744137 lsn[146078201,146078280]:
  wal: xlog heap-update add new tuple: storage: not enough free space in page
```

Reproduced at HEAD `fa90714a` on 2026-07-28 (577 MB of WAL, ~35 segments, i.e.
after retention had run at least once). This is a hard availability defect: a
crash under sustained write load destroys the cluster's ability to start.

The record is 79 bytes — an incremental `xl_heap_update`, not a full-page image.
So redo had to apply it onto the page as reconstructed by the preceding records,
and that reconstruction produced a page with *less* free space than the page the
running server had when it emitted the record.

## 2. Root cause — a sibling-path divergence in the prune redo arm

goopg prunes a heap page in exactly one place, `pagePruneCore`
(`internal/storage/prune.go`), shared by the opportunistic pruner
(`PagePruneOpt`, called from the HOT-update page-full fallback,
`internal/executor/operators_storage.go:3497`) and by VACUUM
(`PageVacuumPrune`). It classifies each dead tuple into one of two outputs:

| tuple | line-pointer outcome | recorded in |
|---|---|---|
| dead HOT chain root (an index entry points at it) | `ItemIDRedirect` → live tip | `PruneResult.Redirects` |
| dead HOT-only tuple / standalone dead tuple | `ItemIDUnused` | `PruneResult.Unused` |

and then compacts. Crucially it compacts on **both** arms:

```go
if len(result.Unused) > 0 {
    vs, err = VacuumHeapPageBySlots(p, result.Unused)
} else {
    // No unused slots but we have redirects: the tuple data for the
    // redirected slots needs to be freed. Run a compaction pass with
    // an empty dead set …
    vs, err = VacuumHeapPageBySlots(p, nil)
}
```

The `else` arm is load-bearing. `VacuumHeapPageBySlots` repacks only the
surviving `ItemIDNormal` tuples down from `pd_special` and resets `pd_upper`; a
line pointer that was just converted to `ItemIDRedirect` is no longer
`ItemIDNormal`, so its **tuple body is not a survivor and its space is
reclaimed**. A prune that produced only redirects therefore still frees space —
and in a pgbench-style HOT workload the redirect-only shape is the common one
(a two-link chain: dead root → live tip, with no dead intermediates to mark
unused).

The redo arm that actually consumes these records today —
`replayDecodedXLogHeapPrune` (`internal/wal/recovery.go`), reached because
`logHeapPruneOpt` emits a PG `xl_heap_prune` via `EncodeHeapPruneOptPG` (change
A7) — guarded the compaction on the unused list alone:

```go
if len(unused) > 0 {
    if _, err := storage.VacuumHeapPageBySlots(page, unused); err != nil { … }
}
if len(redirects) > 0 || len(unused) > 0 {
    storage.MustHeader(page).SetPruneXID(0)
}
```

So on a redirect-only prune redo set the redirect line pointers, cleared
`pd_prune_xid`, stamped `pd_lsn`, and **skipped the repack** — leaving the
redirected roots' tuple bodies in place. From that record onward the replayed
page carried strictly less free space than the page the server had. The next
`xl_heap_update` for that page then failed `PageAddHeapTuple` with
`ErrNoSpaceInPage`, and `ReplayRecords` aborted startup.

The divergence is a *regression introduced by the format switch*: the legacy
native arm, `replayHeapPruneOpt` (same file), calls
`VacuumHeapPageBySlots(page, unused)` **unconditionally** and so has always
matched the runtime. Only the PG-format arm drifted. This is the recurring
"sibling code paths must stay in sync" failure class recorded in
`.ralph/fix_plan.md` Hard-won Rule #2.

## 3. Fix

`replayDecodedXLogHeapPrune` now compacts whenever the record carries any prune
action, mirroring `pagePruneCore`'s condition exactly:

```go
if len(redirects) > 0 || len(unused) > 0 {
    if _, err := storage.VacuumHeapPageBySlots(page, unused); err != nil {
        return fmt.Errorf("wal: xlog heap-prune compact: %w", err)
    }
    storage.MustHeader(page).SetPruneXID(0)
}
```

The condition is deliberately `redirects || unused` rather than unconditional:
goopg emits **freeze** as its own `xl_heap_prune` record (frozen slots, no
redirects, no unused), and the runtime freeze path does not compact. Compacting
a freeze-only record would be a *new* divergence in the opposite direction.

Upstream reference: PG's `heap_xlog_prune_freeze`
(`postgres/src/backend/access/heap/pruneheap.c`) calls `heap_page_prune_execute`
whenever the record has any of the redirect/dead/unused sub-arrays, and
`heap_page_prune_execute` ends in `PageRepairFragmentation` unconditionally —
i.e. upstream also ties the repack to "the record did something", not to the
unused list specifically.

## 4. Verification

- **End-to-end (the defect itself).** `analysis/wal-crash-restart-repro.sh`
  with `LOADSEC=200 KILLAT=170`: at HEAD `fa90714a` the restart fails with
  `xlog heap-update add new tuple: storage: not enough free space in page`
  (577 MB WAL); with the fix the identical run reports `RESTART_OK`
  (593 MB WAL). Same script, same parameters, back-to-back.
- **Unit regression, non-vacuous.**
  `TestReplayPGHeapPruneRedirectOnlyCompactsLikeRuntime`
  (`internal/wal/heap_prune_redirect_only_test.go`) builds a two-tuple HOT chain
  whose prune is redirect-only, prunes a copy at runtime via `PagePruneOpt`,
  replays the encoded `xl_heap_prune` onto the pre-prune page, and asserts the
  replayed page is **byte-identical** to the runtime page apart from `pd_lsn`.
  Negative control (fix reverted): fails with
  `replayed pd_upper = 8112, runtime pd_upper = 8160 — redo did not reclaim the
  redirected root's tuple body`.
- `go test ./internal/wal/ ./internal/storage/` and
  `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — see the commit
  message for the recorded results.

`scripts/tpch-spotcheck.sh` was not run: the change is confined to the WAL redo
path, touching no planner, executor or codec code. The crash-restart repro is
the stronger end-to-end evidence for this class.

## 5. Still open

The redo arm remains **less defensive than upstream** in one respect, recorded
in `.ralph/deferral_ledger.md`: PG's `heap_xlog_update` can always fall back to
a full-page image when the incremental apply cannot hold, because upstream
guarantees an FPI for the first modification of a page after a checkpoint.
goopg's FPI policy (`Pool.needsImage`, `internal/storage/bufpool.go`) implements
the same `pd_lsn <= RedoRecPtr` rule, but nothing in redo *checks* that an
incremental heap-update actually fits — any future runtime/redo divergence of
this class will again surface as an unstartable cluster rather than as a
localised, recoverable error. Making `ErrNoSpaceInPage` during redo a loud,
attributable failure (relation, block, record LSN) instead of a bare wrapped
error is the natural follow-up.

The regress harness's phantom `deferred: cluster restart failed` per case after
a failed restart (root-0032 §5, ledger 2026-07-28) is unchanged by this fix and
remains open.
