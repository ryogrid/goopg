# M0106-0010 step 3dj — pg_proc row for pg_stat_get_wal_receiver

## Context

Step 3di confirmed that the PG-against-goopg SIGSEGV chain that has
dominated M0106-0010 since Step 3da is eliminated.  The PG standby now
walks `starting up replication slots` →
`initializing for hot standby` → `consistent recovery state reached`
→ `database system is ready to accept read-only connections` →
`started streaming WAL from primary` cleanly.

The next blocker is at the SQL layer, not the kernel:

```
ERROR: 42P01: relation "pg_catalog.pg_stat_wal_receiver" does not exist
```

`waitForPhysicalStreamingGoopgToPG` polls
`SELECT status FROM pg_catalog.pg_stat_wal_receiver` on the PG standby
that was cloned from a goopg primary via basebackup. goopg currently
models `pg_stat_wal_receiver` as a virtual view
(`internal/initdb/replication_views.go::registerStatWalReceiverView`,
materialised at runtime by `internal/wal/replmon.go`) — no row exists
in physical `pg_class` / `pg_attribute` / `pg_rewrite`, and the SRF
`pg_stat_get_wal_receiver()` (OID 3317) is not in physical `pg_proc`.
After the standby ingests the basebackup, PG looks up the view by name
in its own catalogs (which were cloned from goopg's catalogs), finds
nothing, and surfaces `42P01`.

Closing the gap requires four physical seeds:

1. `pg_proc` row for `pg_stat_get_wal_receiver` (OID 3317).
2. `pg_class` row for `pg_stat_wal_receiver` (relkind='v') and the
   accompanying `pg_attribute` rows for its 15 columns.
3. `pg_rewrite` row carrying the parser-output query tree for the
   view rule (`SELECT … FROM pg_stat_get_wal_receiver() s WHERE
   s.pid IS NOT NULL`).
4. Populated `proallargtypes` / `proargmodes` / `proargnames` arrays
   on the OID 3317 row so the view's rewrite-rule expansion can
   resolve column references via PG's
   `build_function_result_tupdesc_d()`.

Step 3dj scopes the first piece — a minimal pg_proc row — and leaves
the view-side seed plus populated OUT-arg arrays to Step 3dk.  This
follows the M0106-0010 cadence (each step a narrow, pinned move) and
keeps the row-level regression scope auditable.

## Change

`pgProcEntry` gains four new fields:

| field        | purpose                                                       |
|--------------|---------------------------------------------------------------|
| `Parallel`   | proparallel char. 0 → defaults to `'s'` (safe).               |
| `RetSet`     | proretset bool. Defaults to false (legacy AM-handler shape).  |
| `NotStrict`  | proisstrict inverse. Defaults to false → strict (legacy).     |
| (sentinel)   | `ArgTypes == nil` now means "default to `[internal]`"; an     |
|              | explicit `[]uint32{}` is the unambiguous spelling for a       |
|              | zero-argument function.                                       |

All 31 existing entries (7 AM handlers + 24 type-IO regprocs) are
converted from positional to keyed struct literals so the new fields
do not require touching every row.  The new fields default to the
pre-change values, so every byte of the 31 existing rows is unchanged
— `TestPgProcRowBtreeHandlerMatchesFormPgProc` still passes
unmodified.

`pgProcRow` reads the four new fields:

```go
executor.NewBoolDatum(!e.NotStrict),       // 13 proisstrict
executor.NewBoolDatum(e.RetSet),           // 14 proretset
executor.NewStringDatum(string(parallel)), // 16 proparallel
```

The OID 3317 entry is appended:

```go
{
    OID:         3317,
    Name:        "pg_stat_get_wal_receiver",
    RetType:     2249, // RECORDOID
    ArgTypes:    []uint32{},
    Volatile:    's',
    Parallel:    'r',
    RetSet:      true,
    NotStrict:   true,
    HandlerName: "pg_stat_get_wal_receiver",
}
```

All five non-default fields match
`postgres/src/include/catalog/pg_proc.dat:5668-5675` verbatim.

`bootstrapPgProcOidIndex` already iterates over
`pgProcInitialEntries()` and writes one IndexTuple per entry into
`pg_proc_oid_index` (OID 2690), so the new row gets a populated leaf
slot for free — no changes needed there.

## Why a minimal row in isolation is safe

PG's `SearchSysCache1(PROCOID, 3317)` returns the new tuple, and any
direct dereference of `Form_pg_proc->prosrc`,
`Form_pg_proc->prorettype`, `Form_pg_proc->proretset`, etc. sees the
correct scalar values.  The CATALOG_VARLEN arrays
(`proallargtypes`, `proargmodes`, `proargnames`,
`proargdefaults`, `protrftypes`, `prosrc`, `probin`, `prosqlbody`,
`proconfig`, `proacl`) remain emptyArrayTypeBytes / empty varlena
shells.  PG's view-rewrite path needs the OUT-arg arrays populated,
but that path is not reached until Step 3dk lands the
`pg_stat_wal_receiver` `pg_class` row + `pg_rewrite` rule; until then
the test still trips on `42P01` at the same place.

The cost is one extra heap tuple in `base/{1,5}/1255` and one extra
oid-keyed IndexTuple in `pg_proc_oid_index`. No new wire-protocol
codec work, no new array encoder.

## Regression coverage

Two tests in `internal/initdb/pg_proc_bootstrap_test.go`:

- `TestPgProcInitialEntriesCoverAMHandlers` length pin bumped from
  31 → 32 to admit the new entry; pre-existing per-row assertions
  for AM handlers + type-IO unchanged.
- `TestPgProcRowStatGetWalReceiverIsSRF` (new) pins the
  PG18-canonical byte layout of the OID 3317 heap tuple:
  proisstrict=0, proretset=1, provolatile='s', proparallel='r',
  pronargs=0, prorettype=2249, empty oidvector at proargtypes.

`TestPgProcRowBtreeHandlerMatchesFormPgProc` is the cross-check that
the field-shape refactor did not silently shift any of the 31 legacy
rows.

## Next step

3dk: seed the `pg_stat_wal_receiver` view physically. Requires
- pg_class row at OID assigned per
  `postgres/src/include/catalog/pg_stat_wal_receiver*` (none — view
  is allocated dynamically in upstream; pick a goopg-stable OID),
- 15 pg_attribute rows matching the column-shape of the SELECT list,
- pg_rewrite row containing the parser-output query tree for the
  view's defining SELECT,
- populated proallargtypes / proargmodes / proargnames on the 3317
  pg_proc row (this work requires new array encoders for `oid[]`,
  `char[]`, `text[]` because the current emptyArrayTypeBytes path
  hard-codes empty shells).

The E2E test (`TestE2E_FailoverGoopgToPG/async`) will not advance
until step 3dk completes; step 3dj is foundation work and asserts at
the heap-tuple level only.
