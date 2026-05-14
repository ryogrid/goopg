# 0103-0022 — pg_class.oid numeric OID + relreplident column (M0103-0008 rung 16)

Status: accepted
Milestone: M0103-0008 (Heterogeneous logical-replication failover — Scenario B)

## Context

After rung 15 closed (`gpt.relid` emitted as the relation name to match
the legacy text-typed `pg_class.oid`), lifting `t.Skip` on
`TestPort_PgoutputInteropGoopgToPG` advanced the apply worker connection
but `pg_subscription_rel` stayed empty: tablesync never launched and no
row replicated.

The new failure surface is `fetch_remote_table_info`
(`postgres/src/backend/replication/logical/tablesync.c:825`). Its first
sub-query is:

```sql
SELECT c.oid, c.relreplident, c.relkind
  FROM pg_catalog.pg_class c
  INNER JOIN pg_catalog.pg_namespace n
        ON (c.relnamespace = n.oid)
 WHERE n.nspname = 'public' AND c.relname = 't'
```

declared with `tableRow[] = {OIDOID, CHAROID, CHAROID}`. Then PG reads
`lrel->remoteid = DatumGetObjectId(slot[0])` and plugs the value into the
column-list probe `WHERE gpt.relid = %u`.

Two concrete gaps:

1. **`pg_class.relreplident` does not exist** in goopg's virtual
   `pg_catalog.pg_class`. Even before considering the OID shape, the
   probe fails with `42703: column "relreplident" does not exist` —
   `fetch_remote_table_info` aborts.
2. **`pg_class.oid` was stored as the relation name (text)**. After
   adding the column, the probe parses and executes — but
   `lrel->remoteid = DatumGetObjectId("t")` parses "t" as uint32 → 0,
   and the subsequent column-list LATERAL `WHERE gpt.relid = 0`
   matches nothing.

## Decision

Flip the v0 pg_class.oid convention from "relation name as text" to
"numeric OID as decimal text" and align `pg_get_publication_tables.relid`
with it. Add the missing `relreplident` column with the upstream default
'd' (REPLICA_IDENTITY_DEFAULT). Make `::regclass` cast catalog-aware so
the legacy "name as regclass" comparisons stay correct after the flip.

### Changes

- **`internal/catalog/catalog.go`** — `registerSystemTables` for
  `pg_class`:
  - `oid` column type changes `text` → `oid`.
  - `relnamespace` type changes `text` → `oid` (it has always carried
    the numeric `"2200"`; this just brings the declared type in line so
    the wire type-oid advertisement is correct).
  - `reltoastrelid` type changes `text` → `oid` (cosmetic, same
    rationale).
  - `relpages` type changes `text` → `int4` (cosmetic).
  - New column `relreplident` (char, ordinal 9).
  - `VirtualRows` cell 0 emits `strconv.Itoa(int(t.OID))` instead of
    `t.Name`; cell 9 emits `"d"`.
- **`internal/executor/operators_pg_get_publication_tables.go`** —
  `buildPgGetPublicationTablesRows`: relid now emits
  `NewIntDatum(int64(t.OID))` (rung 15 emitted `NewStringDatum(t.Name)`).
  Unresolved-table corner case keeps `NullDatum`.
- **`internal/executor/expr.go`** — `regclass` cast resolves a
  text relation name to the table's numeric OID via
  `ctx.Catalog.LookupTable`. Numeric inputs pass through. Other
  `reg*` casts are unchanged stubs.

The join `gpt.relid = c.oid` now compares KindInt to KindString-with-
decimal-text. `compareDatum` falls back to `Format()` compare for
mixed kinds; both sides format as the same decimal text so the join
matches. The wire emission for OID-typed columns is text format
(decimal) under `typeOIDFor("oid") = 26`; PG's `oidin` decodes
correctly.

### Why not pure KindInt on both sides

The virtual-catalog path builds each cell via `planner.StringConst`
(`internal/planner/planner.go::buildVirtualValues`) regardless of the
declared column type. Plumbing per-cell-type construction through the
virtual-rows pipeline is out of scope for one rung; the current
mixed-kind comparison is safe because both sides format to the same
decimal text. A future rung may unify on a typed Datum path.

## Tests

- `internal/catalog/catalog_test.go::TestPgClassExposesRelReplident` —
  pins column existence, declared type `char`, and the populated cell
  `"d"`.
- `internal/catalog/catalog_test.go::TestPgClassOidIsNumericOID` —
  pins column type `oid` and cell value equals `strconv.Itoa(t.OID)`.
- `internal/executor/operators_pg_get_publication_tables_test.go::
  TestPgGetPublicationTablesRelidMatchesPgClassOid` — updated from
  "rung 15: both sides text name" to "rung 16: both sides numeric
  OID"; asserts the join row's `relid` cell carries the table's OID
  (`Datum.Int == int64(tbl.OID)`).

## Out of scope

Wire-format encoding for `oid` columns whose Datum is KindString-with-
decimal-text is already correct (AppendValueText emits the raw bytes,
which is a valid OID textual literal). A future rung may move
`pg_class.oid` into a typed Datum pipeline to eliminate the KindString
fallback comparison entirely.

The next live-probe rung will surface the next gap in the libpqrcv
column-list LATERAL or `fetch_remote_table_info` second probe — that
failure mode will be diagnosed and quoted verbatim in the restored
`t.Skip` so the next loop can resume from the exact surface.
