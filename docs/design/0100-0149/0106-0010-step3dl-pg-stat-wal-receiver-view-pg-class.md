# M0106-0010 Step 3dl — Seed `pg_stat_wal_receiver` view in pg_class + pg_attribute

Status: implemented (2026-05-18, loop 1 of Step 3dl)
Predecessor: Step 3dk (`docs/design/0106-0010-step3dk-pg-proc-3317-out-args-arrays.md`)
Successor: Step 3dm (pg_rewrite ev_action seeding — separate loop)

## Goal

After Steps 3dj / 3dk landed `pg_proc` OID 3317
(`pg_stat_get_wal_receiver`) and its 15 OUT-arg metadata arrays
(`proallargtypes` / `proargmodes` / `proargnames`), a PG standby booted
from a goopg data directory can resolve the function's record-type
column list via `build_function_result_tupdesc_d()`. The remaining
visible failure was the E2E test's
`SELECT status FROM pg_catalog.pg_stat_wal_receiver` probe still
returning `42P01 relation "pg_stat_wal_receiver" does not exist`,
because the view itself had no row in goopg's bootstrap `pg_class`
heap.

This step seeds the view's `pg_class` + `pg_attribute` rows so PG's
`RangeVarGetRelid` lookup returns a non-zero OID. The companion
`pg_rewrite` row (with the `ev_action` `pg_node_tree`) is deferred to
Step 3dm because generating a canonical `nodeToString` serialization
of the rewrite Query tree is itself a multi-step problem.

## Changes

### `internal/initdb/initdb.go`

- `pgClassRow` learns a `RelKind == 'v'` branch:
  - `relam = 0`     — views have no table access method
  - `relfilenode = 0` — views have no on-disk storage
    (`RELKIND_HAS_STORAGE` macro in `pg_class.h:200` explicitly
    excludes RELKIND_VIEW).
  - `relhasrules = true` — PG's relcache fetches the ON-SELECT
    rewrite rule from pg_rewrite only when this flag is set; without
    it, a query against the view would silently return zero rows
    once Step 3dm lands the rule.

Existing relkind='r' and relkind='i' branches keep their prior byte
layout (relfilenode = OID, relam = 2 for heap / 403 for btree,
relhasrules = false).

### `internal/initdb/relcache_init.go`

- New `pgStatWalReceiverAttrs()` helper returns the 15
  `nailedAttr` entries for the view's columns. Column order, names,
  and types are verbatim from
  `postgres/src/backend/catalog/system_views.sql:945-963` (which
  projects all 15 OUT-args of `pg_stat_get_wal_receiver()`). Type
  OIDs match `postgres/src/include/catalog/pg_proc.dat:5671`:
  int4=23, text=25, pg_lsn=3220, timestamptz=1184. `attlen` is 4 for
  int4, 8 for pg_lsn and timestamptz, -1 for text. `attnotnull` is
  false on every column because view columns inherit nullability from
  the underlying expression.

- One new entry appended to `nailedLocalRels`:

      {12100, "pg_stat_wal_receiver", 2249, 'v', 15, false, pgStatWalReceiverAttrs()},

  - OID 12100 is a goopg-private stable assignment in PG18's
    `FirstUnpinnedObjectId..FirstNormalObjectId` range
    (12000..16383). PG assigns system_views.sql view OIDs
    dynamically at initdb time, so there is no upstream-canonical OID
    to mirror; 12100 is chosen unilaterally and pinned by regression
    test (`TestNailedLocalRelsContainsPgStatWalReceiver`).
  - RelType=2249 (RECORDOID) matches the underlying function's
    `prorettype` so any code path that follows pg_class.reltype gets
    a valid composite-type pointer (PG always has the anonymous
    RECORD type registered).
  - `IsShared=false` — pg_stat_wal_receiver is a per-database view
    in pg_catalog.
  - The 15 attrs are produced by `pgStatWalReceiverAttrs()`; the
    bootstrap `pgAttrEntriesForRel` fallback (`rel.Attrs`) is used
    directly because this view's OID is not `pg_class` / `pg_attribute`
    itself, so no column-defs-driven derivation is needed.

The view's row passes through the existing `bootstrapPgClassTuples`
and `bootstrapPgAttributeTuples` writers unchanged; both append a
single pg_class row and 15 pg_attribute rows respectively.

### Regression tests

`internal/initdb/pg_stat_wal_receiver_nailed_test.go` (new file):

- `TestNailedLocalRelsContainsPgStatWalReceiver` — pins OID 12100,
  relname, relkind='v', reltype=2249, relnatts=15, IsShared=false, and
  each of the 15 attrs by (Name, TypeOID, Len, Num) plus
  `NotNull==false`. The column order matches system_views.sql:945-963
  byte-for-byte.

- `TestPgClassRowForViewSetsZeroRelfilenode` — calls `pgClassRow` with
  a synthetic `nailedRel` (RelKind='v') and asserts the three
  view-specific overrides: `relam=0`, `relfilenode=0`,
  `relhasrules=true`. This guards against accidental future regression
  in the new `RelKind == 'v'` branch.

## What this unblocks

With the view present in pg_class:

- A PG standby's `RangeVarGetRelid("pg_catalog.pg_stat_wal_receiver")`
  resolves to OID 12100 instead of returning `42P01`.
- `relation_open(12100)` succeeds because `RELKIND_VIEW` skips
  storage-related code paths (`RelationInitTableAccessMethod` early-outs
  for views).
- `SELECT *` planning resolves the 15 column references through
  `pg_attribute`.

## What this does NOT do yet

- `pg_rewrite` is still empty (relhasrules=true but no rule row).
  PG's rewriter, when it expands the view's RangeTblEntry, will fail
  with `ERROR: rule "_RETURN" for view "pg_stat_wal_receiver" does
  not exist` (or, depending on the code path, silently produce a
  no-rows scan). Step 3dm (separate loop) seeds the pg_rewrite row
  with the `nodeToString`-serialized ev_action Query tree.

- `pg_views` (system view that introspects pg_class for relkind='v')
  is not populated; no current test queries it.

## Verification

```
go test -count=1 -run 'TestNailedLocalRelsContainsPgStatWalReceiver|TestPgClassRowForViewSetsZeroRelfilenode' ./internal/initdb/
PASS

go test -count=1 -run 'TestPgProc|TestBootstrapPgProc|TestPgIndex|TestBootstrapPgIndex|TestPgClassOidIndex|TestNailedLocalRels|TestPgClassRowForView|TestPgStatWalReceiver|TestNailedIndexRelnatts|TestMakeBtreeRootPage|TestOidArrayBytes|TestCharArrayBytes|TestTextArrayBytes' ./internal/initdb/
PASS

go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/
PASS
```

The pre-existing baseline failures
(`TestBootstrappedPG{Class,Attribute,Type}RowsReadable` etc., 15 total)
are unchanged; verified via `git stash` round-trip — none are new
regressions from this step.
