(idle — nothing in flight)

M0131-S21a-2 PART 3 LANDED (loop #158) — `XLOG_HEAP2_VISIBLE` + the VM fork.

Files: `internal/wal/{recovery.go,pg_xlog_decode.go}`, new
`internal/storage/vm_redo.go`, new tests
`internal/wal/heap_visible_pg_test.go` + `internal/storage/vm_redo_test.go`,
design `docs/design/0131-0015-pg-wal-opcode-coverage.md` §"S21a-2 … part 3"
(+ README row), fix_plan S21 note, 1 ledger row (4 deferrals).

Key symbols: `replayDecodedXLogHeap2Visible`, `redoVMPageForBlock`,
`redoClearVMBitsForHeapBlock`, `decodeXLogHeapVisibleMainData`,
`storage.VMPageSetBits` / `VMPageClearBits` / `VMPageBits` /
`VMBlockForHeapBlock` / `VMValidBits`.

What landed:
- Every VACUUM's `XLOG_HEAP2_VISIBLE` (0x40) replays: heap page gets
  PD_ALL_VISIBLE, vm page gets the bits, halves independent (a dropped heap
  page must NOT skip the map update).
- `redoVMPageForBlock` = the RBM_ZERO_ON_ERROR member of the
  `redo*PageForBlock` family; an absent vm page is initialised, not a gap.
- No VM handle needed after all: replay runs at `initdb/open.go:380`,
  `VMLoadForks` at `:2472`, so the on-disk fork is the target that survives.
- Part 2's `XLH_LOCK_ALL_FROZEN_CLEARED` deferral discharged, in upstream's
  position — before and independent of the heap redo (so an LSN-skipped lock
  record still fixes the map).
- `VISIBILITYMAP_XLOG_CATALOG_REL` masked off; unknown flag bits refuse.

10 guards / 12 subtests, all proven fail-when-broken by 6 scripted reverts.

Gates: `internal/wal` PASS + `-race` PASS, `internal/storage` PASS,
`internal/initdb` PASS (77 s), UNITS precommit PASS, pgbench smoke via hook.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
items already filed under M-NIGHTLY — parked per banner.

Next loop (banner = M-NIGHTLY filing, then M0131): **S21a-2 part 4** —
`XLOG_HEAP2_LOCK_UPDATED` 0x60 (`heap_xlog_lock_updated`, the multixact
member update; near-sibling of part 2's HEAP_LOCK, so `PageApplyHeapLockRedo`
+ `redoExistingHeapPageForBlock` should both reuse), then `CLOG_ZEROPAGE`
0x00, `SMGR_TRUNCATE` 0x20, `HEAP2_REWRITE`'s refusal. Each landing shrinks
S28's self-arming skip.

In-flight: none.
