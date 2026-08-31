---
id: 0094-0005b-virtual-view-plan-cache-staleness
status: accepted
milestone: M0094-0005
date: 2026-05-14
related: [0094-0005-standby-iterator-tail-anchor, 0005-0001-streaming-replication-architecture]
---

# M0094-0005 — Virtual catalog views must not be served from the plan cache

## Problem

After the tail-anchor fix (0094-0005), `TestReplicationEndToEnd`
still failed: the standby's `pg_stat_wal_receiver.written_lsn`
never advanced past the LSN it held when the walreceiver first
connected, even though the primary's walsender clearly streamed
records and the standby's walreceiver successfully appended them
to local WAL.

The original status note ascribed this to "primary's `WrittenLSN()`
does not advance" — but instrumentation showed the primary writer
*did* advance, the walreceiver *did* call `SetReceivedLSN()`, and a
follow-up `Receivers.Snapshot()` inside the walreceiver *did* return
the freshly-advanced LSN. Yet the `SELECT written_lsn FROM
pg_catalog.pg_stat_wal_receiver` issued from the test still returned
the initial value.

## Root cause

`planner.buildVirtualValues` materialised `tbl.VirtualRows()` into a
fixed `*planner.Values` node at plan time:

```go
func buildVirtualValues(pos int, tbl *catalog.Table, schema Schema) Node {
    raw := tbl.VirtualRows()           // snapshot at plan time
    rows := make([][]Expr, len(raw))
    for i, r := range raw {
        ...
        rows[i] = cells
    }
    return &Values{pos: pos, Rows: rows, schema: schema}
}
```

The server-wide `planCache` (M0098-0005) keys plans by normalised SQL
and serves the cached plan on every subsequent execution. Result: the
first `SELECT … FROM pg_catalog.pg_stat_wal_receiver` baked the LSN-at-
that-moment into the `Values.Rows` slice; every later query with the
same SQL replayed those frozen rows. The plan cache had no idea the
table was a snapshot of mutable state.

This affected every virtual `pg_catalog` view backed by `VirtualRows`:
`pg_stat_replication`, `pg_stat_wal_receiver`, `pg_stat_checkpointer`,
`pg_stat_subscription`, `pg_class`, `pg_namespace`, `pg_database`,
`pg_settings`, `pg_locks`, … all of which return rows that *should*
reflect live state at query time.

## Fix

Two changes, smallest surface that preserves caching for non-virtual
plans:

1. `planner.Values` gains a `VirtualSource *catalog.Table` field.
   `buildVirtualValues` sets it to the source table.
2. `executor.valuesOp` keeps a back-pointer to its `*planner.Values`
   plan. In `Open`, if `VirtualSource` is non-nil, it calls
   `VirtualRows()` and rebuilds `o.rows` with the fresh snapshot via
   a helper `rematerialiseVirtualRows`.

The cached plan is still safe to cache — it now contains a *reference*
to the virtual table rather than the materialised row snapshot. The
executor refreshes rows on every Open, so each query sees current
state. INSERT-side `Values` (no `VirtualSource`) is untouched.

## Verified

- `TestReplicationEndToEnd` — PASS (was: standby received_lsn never
  advanced past 0/69)
- `./internal/planner/...` — PASS
- `./internal/executor/...` — PASS
- `./internal/server/...` — PASS
- `./internal/initdb/...` — PASS
- `./internal/wal/...` — PASS
- `./internal/testutil/replcluster/...` — PASS
- `./internal/testutil/cluster/...` — PASS
- `./internal/catalog/...`, `./internal/parser/...` — PASS

## Scope boundary

`TestE2E_PhysicalReplication` (testport package) still fails — but
the failure mode has shifted from "WAL never streams" to "standby
WAL streams + applies but the standby's executor does not see the
inserted row via SELECT". That is a separate SQL-level row-visibility
problem on the standby (the standby's catalog snapshot, MVCC visibility
of replayed tuples, or buffer-pool reload after WAL apply — to be
diagnosed in a follow-up loop). The original M0094-0005 blocker
("primary's observable `WrittenLSN()` does not advance after CHECKPOINT
in replcluster") is closed by this fix.
