# 0103-0021 — pg_get_publication_tables.relid matches pg_class.oid (rung 15)

Status: accepted (2026-05-14)
Milestone: M0103-0008 (Heterogeneous logical replication, Scenario B — goopg
primary + PG subscriber)
Rung: 15 / probe-survival ladder for `TestPort_PgoutputInteropGoopgToPG`

## Problem

Lifting `t.Skip` after rung 14 produced a new failure mode: the apply worker
connects, decodes every `'w'` frame (acks `recv=0/146` for all four
transactions), but `count(*)` on the subscriber stays at 0. PG's apply worker
silently skips every change because `pg_subscription_rel` is empty, and the
tablesync launcher never runs.

Adding a query-trace `slog.Info` in `handleQuery` showed that CREATE
SUBSCRIPTION sends the PG18 `fetch_table_list` query verbatim
(`subscriptioncmds.c::fetch_table_list`, `server_version >= 160000` branch):

```sql
SELECT DISTINCT n.nspname, c.relname, gpt.attrs
       FROM pg_class c
         JOIN pg_namespace n ON n.oid = c.relnamespace
         JOIN ( SELECT (pg_get_publication_tables(VARIADIC array_agg(pubname::text))).*
                FROM pg_publication
                WHERE pubname IN ( 'p' )) AS gpt
             ON gpt.relid = c.oid
```

The query parsed and executed without error, but returned zero rows.

Root cause: `pg_get_publication_tables` (executor
`operators_pg_get_publication_tables.go::buildPgGetPublicationTablesRows`)
emitted `relid` as `NewIntDatum(int64(t.OID))`, while goopg's virtual
`pg_catalog.pg_class.oid` column (`catalog.go::registerSystemTables`) stores
the relation *name* as text (the established v0 convention so pgbench's
`oid = $1::pg_catalog.regclass` works — see the design note at
`catalog.go:707-712`: "regclass casts are no-ops in v0 — pgbench's
`oid=$1::pg_catalog.regclass` ends up comparing the bound text parameter (the
table name) against pg_class.oid").

`compareDatum(KindInt, KindString)` falls back to `strings.Compare(a.Format(),
b.Format())` (`expr.go:620-680`), so the join evaluates `"16384" = "t"` — never
true. No type-mismatch error; the join silently dropped every row.

## Fix

In `buildPgGetPublicationTablesRows` (one SRF; reused by both the FROM-clause
operator and the ProjectSet path), emit `relid` as `NewStringDatum(t.Name)`
instead of `NewIntDatum(int64(t.OID))`. NULL is reserved for the corner case of
an unresolved table (empty `Name`).

This aligns the SRF with the established v0 catalog convention: every place
that compares against `pg_class.oid` (the regclass-as-name compatibility shim)
now sees the same scalar shape. CREATE SUBSCRIPTION's `fetch_table_list`
JOIN succeeds, `pg_subscription_rel` is populated, and the tablesync launcher
fires.

The declared SRF schema (`planner.go:1459`) still names `relid` as type `oid`;
that's a column-name/type-name advertisement rather than a Datum-kind
contract, and downstream Datum operations work on Kind+value, not declared
type.

### Scope decision: why not the symmetric reverse fix?

The reverse — change `pg_class.oid` to the numeric OID and make `::regclass`
do a name → OID lookup — would be more PG-faithful but requires touching the
`regclass` cast path, every other virtual catalog that stores OIDs (today they
all use the same "name as text" convention), and pgbench's compatibility
surface. That's out of scope for a single rung; deferred to a future
catalog-OID consistency pass if/when the convention itself is revisited.

## Pinned by

`TestPgGetPublicationTablesRelidMatchesPgClassOid` in
`internal/executor/operators_pg_get_publication_tables_test.go`. It registers
a user table, creates a publication, runs the exact join shape from PG's
`fetch_table_list` (with the column-list / namespace subqueries reduced to
the join condition under test), and asserts a non-empty result set with
`gpt.relid` equal to `c.oid`.

## Next rung (deferred)

With this fix in place, the live probe is expected to surface the next
gap: tablesync's `fetch_remote_table_info` (`tablesync.c:825`) first issues
`SELECT c.oid, c.relreplident, c.relkind FROM pg_class c JOIN pg_namespace n
ON c.relnamespace = n.oid WHERE n.nspname='%s' AND c.relname='%s'` with
`tableRow[] = {OIDOID, CHAROID, CHAROID}`. goopg sends `c.oid` as the relation
name (text) but libpqrcv expects a numeric OID via `DatumGetObjectId`, so
`lrel->remoteid` decodes to 0 and the subsequent `WHERE gpt.relid = 0` query
returns zero rows again. That's rung 16's natural surface; closing it likely
requires either a wire-mode coercion path or a per-row format hint for
`pg_class.oid` when accessed through PG's tableRow-typed result decoder.
