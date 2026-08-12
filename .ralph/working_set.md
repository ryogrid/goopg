(idle — nothing in flight)

M0131-S21c LANDED (loop #168) — in-place line-pointer reuse in PG-format heap redo.

Files: `internal/wal/recovery.go` (new `redoHeapPageAddItemOverwrite`; all three
call sites rewired), `internal/wal/heap_multi_insert_pg_test.go` (1 test renamed
+ 2 new), design `docs/design/0131-0015-*` §"S21c" + Guards, `.ralph/fix_plan.md`
(S21c checked, S21d filed), 1 `.ralph/deferral_ledger.md` row.

The discovery worth keeping: ONE upstream call —
`PageAddItemExtended(..., offnum, PAI_OVERWRITE | PAI_IS_HEAP)` — is shared by
`heap_xlog_insert`, `heap_xlog_multi_insert` AND `heap_xlog_update`, and goopg's
three copies disagreed three different ways:
1. multi-insert REFUSED the already-allocated case (the known S21c symptom);
2. single insert had **no check at all** — `PageInsertItemRawAt` SHIFTED the
   array, moving the row at the target slot and staling every ctid to it. Silent
   corruption where upstream PANICs; found only by the sibling-path rule;
3. update ignored `new_offnum` entirely, APPENDED, then rejected the result as
   "new-slot drift".
LP_UNUSED target → fill in place; USED target → hard refusal (upstream WARNING →
caller PANIC). The in-place case is ordinary traffic: any COPY after a VACUUM.

S28 gate moved forward, still red: `TestE2E_GoopgCrashStartOnPGDataDir` went
from refusing at replay record 24720 → 43900, now stopping at
"xlog heap-update: block 41 is uninitialised". That is a DIFFERENT defect —
`replayDecodedXLogHeapUpdate` never got S21a-2's `redoHeapPageForBlock`
(zero-extend past the replay gap + `XLOG_HEAP_INIT_PAGE`) — filed as
**M0131-S21d** with a ledger row.

Gates: `internal/wal` full package PASS, `internal/storage` PASS, `-race` on the
touched wal tests PASS, UNITS precommit PASS (warm cache; `internal/initdb` 81s
cold), pgbench smoke via the commit hook, `make ralph-state-guard` OK.
Fail-when-broken proven by a scripted revert of the reuse branch → both new
subtests FAIL.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY, nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): **M0131-S21d** — it is the
S28 gate's current stop and a ~1-loop mechanical fix. Otherwise S23 (cheap tail).

In-flight: none.
