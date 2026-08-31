# Design 0103-0020 — `pg_class.relnatts` column for CREATE SUBSCRIPTION column-list probe

**Status:** accepted (2026-05-14)

**Milestone:** M0103-0008 rung 14 — live-probe ladder against
`TestPort_PgoutputInteropGoopgToPG`.

**Closes:** `fetch_table_list_from_publisher` executor-side failure
that surfaced after rung 13 (0103-0019) unblocked the LATERAL
`pg_catalog`-qualified SRF parser dispatch. With the parser path
clear, the same probe reached the executor and raised SQLSTATE
42703 — `column "relnatts" does not exist` — preventing CREATE
SUBSCRIPTION from populating `pg_subscription_rel` on a PG
subscriber pointed at a goopg publisher.

## Problem

PG's `fetch_table_list_from_publisher`
(`postgres/src/backend/commands/subscriptioncmds.c`) is shaped as:

```sql
SELECT DISTINCT t.schemaname, t.tablename
  , (CASE WHEN (array_length(gpt.attrs, 1) = c.relnatts)
         THEN NULL ELSE gpt.attrs END)
  , gpt.qual
FROM pg_catalog.pg_publication_tables t
LEFT OUTER JOIN pg_catalog.pg_class c
  ON c.relname = t.tablename
 AND c.relnamespace = (SELECT oid FROM pg_catalog.pg_namespace
                       WHERE nspname = t.schemaname)
LATERAL pg_catalog.pg_get_publication_tables(t.pubname) AS gpt
WHERE t.pubname IN (…)
```

The `array_length(gpt.attrs, 1) = c.relnatts` test is how PG
decides whether the publisher restricted the column set: when the
published column count equals the relation's full natts, no
filtering is needed and `attrs` is NULLed out.

goopg's `pg_class` virtual view (in `internal/catalog/catalog.go::
registerSystemTables`) listed eight columns (`oid`, `relname`,
`relkind`, `relnamespace`, `relpersistence`, `reltoastrelid`,
`relpages`, `relispopulated`). `relnatts` was missing entirely.
Catalog-codec persistence (`internal/catalog/codec.go`) already
modelled `RelNAtts` for on-disk pg_class rows — but the virtual
view used by SQL-side queries against `pg_catalog.pg_class` was
a separate construct and had never been extended.

The failure mode was silent for the same reason as rung 13: PG's
apply worker doesn't surface fetch_table_list errors back through
the libpq stream; the subscription just lands with zero rel rows
and every change is dropped by `should_apply_changes_for_rel`.

## Approach

Extend the virtual `pg_class` schema with a ninth column
`relnatts int4` at ordinal 8, and populate every row with
`strconv.Itoa(len(t.Columns))`. goopg has no system columns in
its catalog (no rowid / ctid / oid_column), so user-column count
is the value PG would compute as `relnatts` for an equivalent
relation under default settings (where system columns are
excluded from `pg_class.relnatts` even upstream — see
`heap_create`).

The virtual view's RelOid (`1259`, `RelationRelationId`) and
ordering of the existing columns are preserved so every existing
caller — pgbench's `regclass` probe, vacuumdb's namespace join,
HammerDB's schema check — stays byte-identical at the column-
position level.

No code paths read `t.Columns` in a way that would race with
this loop: the read happens inside the
`VirtualRows` closure that already holds `c.mu.RLock()` for the
duration of the snapshot build.

## Changes

- `internal/catalog/catalog.go`:
  - `import "strconv"` added.
  - `pgClass.Columns` gains a 9th entry
    `{Name: "relnatts", Type: Type{Name: "int4"}, Ordinal: 8}`.
  - The `VirtualRows` closure appends `strconv.Itoa(len(t.Columns))`
    as the 9th cell of every row.

## Tests

- `internal/catalog/catalog_test.go::TestPgClassExposesRelNatts`
  registers a single user table `t(id int4, v text)`, looks up
  `pg_catalog.pg_class`, asserts the `relnatts` column exists at
  ordinal 8 with type `int4`, and asserts the row's relnatts cell
  is `"2"` (matches user-column count).

## Rung-15 outlook

With `relnatts` available the column-list probe should parse,
plan, and execute against a goopg publisher. The next likely
surface is `pg_publication_tables` virtual view column
exposure — upstream's column list (`pubname`, `schemaname`,
`tablename`, `attnames`, `rowfilter`) may or may not be fully
present in goopg's view. That gets its own rung (and design
doc) when surfaced by a live probe.

The `t.Skip` on `TestPort_PgoutputInteropGoopgToPG` stays in
place so the next rung lands with its own design doc + targeted
unit pin per the rung protocol established in 0103-0010 through
0103-0019.

## Verification

- `go test -race -count=1 -timeout 300s ./internal/catalog/
  ./internal/planner/ ./internal/analyzer/ ./internal/executor/
  ./internal/server/ ./internal/wal/ ./internal/storage/` →
  recorded green in fix_plan.md M0103-0008 rung-14 closure note.
