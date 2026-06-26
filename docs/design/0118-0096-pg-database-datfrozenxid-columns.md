# 0118-0096 — `pg_database.datfrozenxid` / `datminmxid` catalog-parity columns

**Status:** accepted
**Milestone:** M0118 (isolation spec pass-through) — `intra-grant-inplace-db`
enabler; also M0117-0008 (datfrozenxid) catalog-surface alignment.
**Type:** Enabler (NOT a spec promotion). Bounded virtual-catalog column addition.

## Problem

The `intra-grant-inplace-db.spec` step `snap3` runs:

```sql
INSERT INTO frozen_witness
  SELECT datfrozenxid FROM pg_database WHERE datname = current_catalog;
```

goopg's `pg_database` virtual catalog (`internal/catalog/catalog.go`) projected
only `oid, datname, datdba, encoding, datallowconn, datconnlimit, datistemplate`,
so the query failed with `ERROR: column "datfrozenxid" does not exist (42703)` —
the spec's first divergence, ahead of its hard blocker (a `VACUUM (FREEZE)`
in-place update of the `pg_database` row that must wait behind a concurrent
`GRANT TEMP ON DATABASE …`).

`datfrozenxid` is a standard `pg_database` column that real monitoring queries
read constantly (`SELECT datname, age(datfrozenxid) FROM pg_database`), and goopg
*already computes* the value — `InMemory.DatFrozenXID()` returns the cluster-wide
`min(relfrozenxid)` across user heaps (mirroring `vac_update_datfrozenxid`,
consumed by the CLOG-truncation horizon in `initdb/open.go`) — but never exposed
it through the catalog. So this is a genuine catalog-parity gap independent of the
isolation spec.

## Change

Add two columns to the `pg_catalog.pg_database` virtual table:

| column | type | value |
|---|---|---|
| `datfrozenxid` | `xid` | `DatFrozenXID()`, or bootstrap `FrozenTransactionID` (2) when no user heap is frozen yet |
| `datminmxid` | `xid` | `FirstMultiXactId` (1) — goopg never advances a per-database multixact-freeze horizon, so 1 is the accurate floor |

`VirtualRows` computes `datfrozenxid` once per call (outside the per-database
loop) since the value is cluster-wide today, and appends both as trailing
columns to every database row. `DatFrozenXID()` takes its own `RLock`; the
closure does not hold `c.mu` (it already calls `ListDatabases()` the same way),
so there is no nested-lock hazard.

When `DatFrozenXID()` returns `InvalidTransactionID` (0 — no user heap frozen
yet), the column reports `FrozenTransactionID` (2) rather than a non-existent
XID 0, matching PG's fresh-database `datfrozenxid`.

## Scope / blast radius

- Pure additive trailing columns on a virtual table. Existing consumers query
  `pg_database` by **column name** (`datname`, `datallowconn`, `datconnlimit`
  for `pg_amcheck`/`vacuumdb`), so the new ordinals 7/8 do not perturb them.
- No MVCC / storage / WAL surface. No engine-path behaviour change.

## What this does NOT do (deferred — ledger 2026-06-25)

`intra-grant-inplace-db` is **not** promoted. Its remaining blocker is the
hard one: `VACUUM (FREEZE)` and `GRANT … ON DATABASE` both perform an
in-place update of the shared `pg_database` row (`global/1262`), and the spec
asserts `VACUUM (FREEZE)` *waits* (`<waiting ...>`) behind the uncommitted
GRANT's lock on that row. goopg has no runtime shared-catalog
`heap_inplace_update` / row-level lock on `pg_database` (see
`goopg_no_runtime_shared_catalog_inplace_update`), and `datfrozenxid` is served
from the in-memory horizon rather than a lockable heap tuple. The sibling
`intra-grant-inplace` needs the same capability for `pg_class`. Both stay
`failed` until that shared-catalog MVCC-tuple subsystem lands.

## Gates

- `TestPgDatabaseExposesFrozenXidColumns` (catalog unit) — pins both columns +
  bootstrap values 2 / 1.
- Full `internal/catalog` package PASS (no positional-consumer regression).
- Live end-to-end: `SELECT datname, datfrozenxid, datminmxid FROM pg_database
  WHERE datname = current_catalog` → `postgres 2 1` against a running cluster.
- `go build ./...` clean; pgbench smoke = pre-commit hook.
