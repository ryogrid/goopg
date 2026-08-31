# M0106-0010 Step 3dc(1): pg_proc I/O regproc heap rows

**Status:** Landed 2026-05-18
**Spec:** M0106-0010 (post Step 3db)
**Dependencies:** Step 3da (pg_type I/O regproc OIDs); Step 3db (pg_proc_oid_index populated 2-page btree)

## Context

After Step 3db landed a populated `pg_proc_oid_index` (2-page btree)
the E2E `[m0102-pg-standby-log]` capture still showed one
`signal 11: Segmentation fault` line. The standby's `aio_shared_buffer_readv_cb`
cycle inside `InitPostgres` (`postinit.c:723`) was reaching a SysCache
lookup whose `pg_proc_oid_index` *probe* now succeeded — but the
heap-tuple pointer the IndexTuple referred to was for an OID that was
not in the seed (`pgProcInitialEntries` covered only the seven AM
handlers). `fmgr_info` thus received a NULL tuple from
`SearchSysCache1(PROCOID, ...)` and dereferenced it.

The previously-unexercised OIDs are the I/O regprocs that Step 3da
wired into the `typinput / typoutput / typreceive / typsend` columns of
the canonical pg_type rows. `fmgr_isbuiltin` cannot short-circuit them —
all `internal`-language builtins still go through the catcache /
pg_proc heap path.

## Decision

Extend `pgProcInitialEntries()` with the type-I/O regproc rows for the
core type set referenced by nailed pg_type entries:

| Type | in | out | recv | send |
|------|----|----|------|------|
| bool (16) | 1242 boolin | 1243 boolout | 2436 boolrecv | 2437 boolsend |
| name (19) | 34 namein | 35 nameout | 2422 namerecv | 2423 namesend |
| int4 (23) | 42 int4in | 43 int4out | 2406 int4recv | 2407 int4send |
| text (25) | 46 textin | 47 textout | 2414 textrecv | 2415 textsend |
| oid  (26) | 1798 oidin | 1799 oidout | 2418 oidrecv | 2419 oidsend |
| anyarray (2277) | 750 array_in | 751 array_out | 2400 array_recv | 2401 array_send |

All `prorettype` / `proargtypes` / `provolatile` values are taken
verbatim from `postgres/src/include/catalog/pg_proc.dat`. text/name
`*recv` and `*send`, and the entire array I/O quad, are PROVOLATILE 's'
upstream; everything else is 'v'.

`array_in` and `array_recv` take three arguments
(`cstring|internal, oid, int4`) — the existing single-element
`(internal)` oidvector encoding in `pgProcRow` is generalised to use
the entry's `ArgTypes` slice and update `pronargs` accordingly.

## Implementation notes

* `pgProcEntry` gains `ArgTypes []uint32` and `Volatile byte` fields.
* `pgProcRow` defaults a nil/empty `ArgTypes` to `[2281]` (internal)
  and a zero `Volatile` to `'v'` so the existing AM-handler entries
  encode byte-identically to the pre-change layout — the
  `TestPgProcRowBtreeHandlerMatchesFormPgProc` byte-offset pins stay
  valid without edit.
* `bootstrapPgProcOidIndex` already iterates over
  `pgProcInitialEntries` and a single leaf page comfortably holds 31
  IndexTuples (8 bytes header + 4 bytes key per entry).
* `bootstrapPgProcTuples` (via `writeMultiPageHeapRows`) transparently
  grows to a second BlockSize page when 31 heap rows no longer fit in
  one. The `TestBootstrapPgProcTuplesWritesRowsToBase1And5` page-size
  check is relaxed from `== BlockSize` to "non-zero multiple of
  BlockSize" (the load-bearing invariant is page alignment, not
  one-page).

## Regression pins

`internal/initdb/pg_proc_bootstrap_test.go::TestPgProcInitialEntriesCoverAMHandlers`
is rewritten to:
1. Pin total entry count at 31 (7 AM handlers + 24 I/O regprocs).
2. Reject duplicate OIDs.
3. Pin every I/O regproc's `proname`, `prorettype`, `proargtypes`,
   `provolatile` to its `pg_proc.dat` values.

`TestPgProcRowBtreeHandlerMatchesFormPgProc` is unchanged — defaults
preserve the bthandler byte layout it pins.

`TestBootstrapPgProcOidIndexWritesPopulatedBtree` is unchanged — it
already iterates `len(tids)`, validates strictly ascending OID keys,
and confirms the bthandler (OID 330) canary leaf.

## Verification

```
go build ./...                                                # PASS
go test -count=1 -run 'TestPgProc|TestBootstrapPgProc' \
       ./internal/initdb/                                     # PASS
go test -count=1 ./internal/initdb/                           # 15 pre-existing baseline failures only
go test -count=1 ./internal/executor/ ./internal/server/ \
       ./internal/storage/ ./internal/catalog/ ./internal/mvcc/  # PASS
```

The 15 pre-existing baseline failures (`TestMigration*`,
`TestCommittedTableSurvivesCrashRestart`,
`TestRuntimeCloseTriggersFinalCheckpoint`,
`TestBootstrappedPG{Class,Attribute,Type}RowsReadable`,
`TestSynchronousCommitFlushesByDefault`, …) were confirmed unchanged
via a `git stash` round trip — no new regressions from Step 3dc(1).

## E2E impact

`[m0102-pg-standby-log]` capture comparison (after vs Step 3db
baseline) — captured by `TestE2E_FailoverGoopgToPG` with
`GOOPG_RUN_BLOCKED_M0102_E2E=1`. Counts captured in the post-commit log
appended to the fix-plan entry.

## Open follow-ups

* If the SIGSEGV survives even with the heap seed in place, the
  fallback path is the `tools/segv_backtrace/` `LD_PRELOAD` shim
  described in the Step 3db fix-plan notes (Step 3dc fallback (2)).
* Wider type coverage: bytea (17), char (18), int2 (21), oidvector
  (30), int2vector (22), and the various aclitem/regproc I/O OIDs may
  need analogous rows when their type rows become reachable via the
  SysCache miss path. Land lazily as new SIGSEGVs surface.
