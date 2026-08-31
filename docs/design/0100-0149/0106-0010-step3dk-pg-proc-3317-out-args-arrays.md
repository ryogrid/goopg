# M0106-0010 Step 3dk — `pg_proc` OID 3317 OUT-args arrays

## Problem

After Step 3dj seeded the `pg_stat_get_wal_receiver` (OID 3317) `pg_proc`
row with `proretset=true`, the three `CATALOG_VARLEN` array columns
describing its OUT args remained empty-`ArrayType` shells:

* `proallargtypes` (oid[]) — types of all 15 OUT-args
* `proargmodes`    (char[]) — per-arg direction code (`'o'` × 15)
* `proargnames`    (text[]) — per-arg name

PG's `build_function_result_tupdesc_d()`
(`postgres/src/backend/utils/funcapi.c`) consults exactly these three
arrays to materialise the `RECORD` shape returned by an OUT-args SRF.
Until the arrays are present, the upcoming `pg_stat_wal_receiver` view
rewrite rule (Step 3dl) cannot resolve `s.<col>` references — the parser
asks `get_func_result_name` / `internal_get_result_type`, which read
these columns, and currently get back zero-element shells.

## Source of truth

`postgres/src/include/catalog/pg_proc.dat:5668-5674` (verbatim):

```
{ oid => '3317',
  proname => 'pg_stat_get_wal_receiver', proisstrict => 'f', provolatile => 's',
  proparallel => 'r', prorettype => 'record', proargtypes => '',
  proallargtypes => '{int4,text,pg_lsn,int4,pg_lsn,pg_lsn,int4,timestamptz,timestamptz,pg_lsn,timestamptz,text,text,int4,text}',
  proargmodes => '{o,o,o,o,o,o,o,o,o,o,o,o,o,o,o}',
  proargnames => '{pid,status,receive_start_lsn,receive_start_tli,written_lsn,flushed_lsn,received_tli,last_msg_send_time,last_msg_receipt_time,latest_end_lsn,latest_end_time,slot_name,sender_host,sender_port,conninfo}',
  prosrc => 'pg_stat_get_wal_receiver' },
```

Translating the type names to OIDs (from `postgres/src/include/catalog/pg_type_d.h`):

| name        | OID  |
|-------------|------|
| int4        | 23   |
| text        | 25   |
| pg_lsn      | 3220 |
| timestamptz | 1184 |

## Design

Three layers cooperate.

### 1. `encodeValuePG` — `KindBytes` passthrough for array types

`internal/executor/codec.go::encodeValuePG` already handled five binary
array types (`aclitem[]`, `text[]`, `oid[]`, `int2[]`, `char[]`) by
returning a 16-byte empty `ArrayType` blob from `emptyArrayTypeBytes`.
Each branch now first checks `d.Kind == KindBytes`; if so it returns the
caller-supplied blob verbatim. The empty-array fallback still fires for
`NewStringDatum("")` (back-compat with every pre-Step-3dk consumer).

### 2. New array encoders in `internal/initdb/initdb.go`

Three helpers, each producing a PG-canonical 1-D `ArrayType`:

* `oidArrayBytes([]uint32) []byte` — 24-byte header
  (`vl_len_=(total<<2)`, `ndim=1`, `dataoffset=0`, `elemtype=26`,
  `dim[0]=N`, `lbound[0]=1`) + `N×4` LE OIDs.
* `charArrayBytes([]byte) []byte` — same header shape with
  `elemtype=18`, no inter-element padding (`typalign='c'`).
* `textArrayBytes([]string) []byte` — header with `elemtype=25`,
  each element prefixed by a 4-byte `SET_VARSIZE_4B` header
  (`(4+len(s))<<2`), with `(off+3) &^ 3` alignment between elements
  (`typalign='i'`, matching PG's `array_seek`).

All three differ from the existing `oidVectorBytes` only in `lbound`
(arrays use `1`, vectors use `0`) and in the per-element layout.

### 3. `pgProcEntry` / `pgProcRow` wiring

`pgProcEntry` gains three optional fields:

```go
AllArgTypes []uint32 // proallargtypes (oid[])
ArgModes    []byte   // proargmodes (char[])
ArgNames    []string // proargnames (text[])
```

`pgProcRow` chooses per-column between `NewStringDatum("")`
(empty-array fallback, current behaviour for every other entry) and
`NewBytesDatum(<helper>(...))` (binary `ArrayType` passthrough). The
OID 3317 entry populates all three fields verbatim from `pg_proc.dat`.

## Regression pins

`internal/initdb/pg_proc_outargs_test.go` adds four tests:

* `TestOidArrayBytesShapeMatchesPGConstructArray` — pins
  `oidArrayBytes([23, 25, 3220])` byte layout (36-byte total,
  24-byte header, elemtype=26, lbound=1, payload).
* `TestCharArrayBytesShapeMatchesPGConstructArray` — pins
  `charArrayBytes(['o','i','b'])` (27 bytes, packed payload).
* `TestTextArrayBytesShapeMatchesPGConstructArray` — pins
  `textArrayBytes(["a","bb","ccc"])` (47 bytes, including
  4-byte alignment padding between varlena elements).
* `TestPgProcRowStatGetWalReceiverOutArgsMatchPgProcDat` — pins the
  15-element `(AllArgTypes, ArgModes, ArgNames)` triple on the
  `pgProcInitialEntries()` row for OID 3317 against the canonical
  values from `pg_proc.dat:5671-5673`.

The previous Step 3dj pin
(`TestPgProcRowStatGetWalReceiverIsSRF`) remains unmodified; the
proargtypes column it inspects at offset 112 still encodes an empty
oidvector regardless of the new OUT-args populating columns 21–23
downstream in the heap tuple.

## Verification

* `go test -count=1 -run 'TestOidArrayBytes|TestCharArrayBytes|TestTextArrayBytes|TestPgProcRowStatGetWalReceiver' ./internal/initdb/` — PASS (4 new tests).
* `go test -count=1 -run 'TestPgProc|TestBootstrapPgProc|TestPgIndex|TestBootstrapPgIndex|TestNailedIndexRelnatts|TestPgClassOidIndex|TestMakeBtreeRootPage' ./internal/initdb/` — PASS (all prior pins still green).
* `go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.
* `go test -count=1 ./internal/initdb/` — 15 pre-existing baseline
  failures (TestBootstrappedPG{Class,Attribute,Type}RowsReadable,
  TestCommittedTableSurvivesCrashRestart,
  TestCreateIndex{Recovered…,SurvivesRestart…},
  TestCreateTableSurvivesRestartViaCatalogHeap,
  TestMigration{FromLegacyJSON…,Idempotent,PGAttributeRowsWritten},
  TestMultipleTablesLoadFromHeap,
  TestOpenOldClusterWithoutM0030…,
  TestRuntimeCloseTriggersFinalCheckpoint,
  TestSynchronousCommitFlushesByDefault,
  TestSystemCatalogRelfilesAreValidHeapPages) — unchanged via
  `git stash` round-trip; no new regressions.

## Next step

**Step 3dl**: seed the view side. With the OUT-args metadata in place,
`build_function_result_tupdesc_d()` can resolve `s.<col>` references —
but the view itself still needs three more layers:

1. A `pg_class` row with `relkind='v'` (goopg-stable OID) for
   `pg_stat_wal_receiver`.
2. 15 `pg_attribute` rows matching the SELECT list in
   `postgres/src/backend/catalog/system_views.sql:945-963`.
3. A `pg_rewrite` row carrying the parser-output query tree for
   `SELECT … FROM pg_stat_get_wal_receiver() s WHERE s.pid IS NOT NULL`.

Until 3dl lands, the E2E test still surfaces `42P01: relation
pg_stat_wal_receiver does not exist`. Step 3dk is foundational for 3dl —
the view's column list resolves through the arrays seeded here.
