# 0094-0004 — Subscription TAP Test Porting Strategy (D-004 Subset)

**Status:** draft
**Date:** 2026-05-11
**Milestone:** M0094-0004

## Background

`D-004` in `docs/test-port/postgres-oracle-port-status.csv` defers all 36 TAP
tests under `postgres/src/test/subscription/t/` pending logical replication
capability growth. M0094-0002 implements DELETE and UPDATE in the apply worker.
This doc defines which tests to port first and how to adapt them.

## Selection Criteria

A subscription test is portable in M0094 if:

1. It only exercises INSERT, DELETE, and UPDATE (not TRUNCATE, DDL, sequences,
   or large objects).
2. It does not require: binary format, pgoutput v2 streaming, two-phase commit,
   row filters, column lists, schema publications, non-deterministic collations,
   partitioned tables, or multi-encoding.
3. The publisher and subscriber are both goopg instances (no cross-version compat).

## Ported Tests

### TestPort_Subscription001RepChanges
**Upstream:** `postgres/src/test/subscription/t/001_rep_changes.pl`
**Rationale:** The canonical logical replication smoke test. Creates a publication
and subscription, verifies INSERT, UPDATE, and DELETE are replicated. Also tests
`REPLICA IDENTITY NOTHING` (INSERT-only replication).

**Key assertions:**
- INSERT on publisher → row appears on subscriber.
- DELETE on publisher → row disappears on subscriber.
- UPDATE on publisher → row value changes on subscriber.
- With `REPLICA IDENTITY NOTHING`: UPDATE/DELETE on publisher return an error
  ("cannot delete from a table with no replica identity").

**Adaptation notes:**
- `$publisher->safe_psql(...)` → `pub.Exec(ctx, ...)`
- `$subscriber->poll_query_until(...)` → polling loop on subscriber connection.
- `REPLICA IDENTITY NOTHING` test verifies the publisher-side error, not
  subscriber state.
- Upstream test sets `wal_level=logical`; goopg always uses logical-compatible
  WAL, no GUC needed.

### TestPort_Subscription004Sync
**Upstream:** `postgres/src/test/subscription/t/004_sync.pl`
**Rationale:** Tests initial table synchronisation: when a subscription is created
against a non-empty table, the sync worker copies the existing rows and then
hands off to streaming. Verifies the copy+catchup handoff produces no gaps and
no duplicates.

**Key assertions:**
- Pre-populate publisher table with N rows.
- Create subscription; wait for initial sync completion (`srsubstate = 'r'`).
- All N rows are present on subscriber (no gaps).
- INSERT on publisher after sync → row appears on subscriber (no gap in streaming).
- No duplicate rows on subscriber.

**Adaptation notes:**
- Check `pg_subscription_rel.srsubstate = 'r'` (ready) to detect sync completion.
  Use a polling loop on the subscriber.
- The sync worker in goopg is wired in `logicalreceiver.go`. Verify it sets
  `srsubstate` correctly on completion.

### TestPort_Subscription026Stats
**Upstream:** `postgres/src/test/subscription/t/026_stats.pl`
**Rationale:** Tests that `pg_stat_subscription` and `pg_stat_replication` are
populated with accurate metrics during active replication.

**Key assertions:**
- `pg_stat_subscription` has a row for the active subscription with:
  - `subenabled = true`
  - `received_lsn` increasing as inserts flow through.
  - `last_msg_receipt_time` non-null.
- `pg_stat_replication` on the publisher has a row with non-null `sent_lsn`,
  `write_lsn`, `flush_lsn`, `replay_lsn`.

**Adaptation notes:**
- Query subscriber's `pg_stat_subscription` and publisher's `pg_stat_replication`
  after a batch of INSERTs.
- Allow a 5-second polling window for stats to be updated after apply.
- Do not assert exact LSN values; verify they are non-null and advancing.

## Port Pattern

All tests live in `internal/testport/subscription_port_test.go`. The file starts with:

```go
// Package testport contains oracle TAP test ports for goopg.
// Run: go test -v -run TestPort_Subscription ./internal/testport/
package testport
```

Each test uses a publisher + subscriber pair:

```go
// TestPort_Subscription001RepChanges ports
// postgres/src/test/subscription/t/001_rep_changes.pl
func TestPort_Subscription001RepChanges(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping logical replication test in short mode")
    }
    ctx := context.Background()

    // Start publisher.
    pub := framework.NewCluster(t, "pub", framework.Options{...})
    defer pub.Stop()

    // Start subscriber.
    sub := framework.NewCluster(t, "sub", framework.Options{...})
    defer sub.Stop()

    // Create publication on publisher.
    pub.MustExec(ctx, t, "CREATE TABLE rep_t (id int primary key, val text)")
    pub.MustExec(ctx, t, "CREATE PUBLICATION pub_rep FOR TABLE rep_t")

    // Create subscription on subscriber.
    sub.MustExec(ctx, t, "CREATE TABLE rep_t (id int primary key, val text)")
    sub.MustExec(ctx, t, fmt.Sprintf(
        "CREATE SUBSCRIPTION sub_rep CONNECTION '%s' PUBLICATION pub_rep",
        pub.ConnectionString(),
    ))

    // Insert on publisher; verify on subscriber.
    pub.MustExec(ctx, t, "INSERT INTO rep_t VALUES (1, 'hello')")
    pollUntil(t, 15*time.Second, func() bool {
        rows, _ := sub.Query(ctx, "SELECT val FROM rep_t WHERE id = 1")
        return len(rows) == 1 && rows[0][0] == "hello"
    })

    // ... DELETE and UPDATE assertions ...
}
```

Run explicitly:

```bash
go test -v -run TestPort_Subscription ./internal/testport/
go test -v -run TestPort_Subscription001RepChanges ./internal/testport/
```

## Deferred Tests (Out of Scope for M0094)

| Test | Deferral reason |
|------|----------------|
| 002_types | Complex datatypes: arrays, range types, composite types |
| 003_constraints | Requires UPDATE/DELETE (deferred to M0094-0002 completion) |
| 005_encoding | Multi-encoding: goopg is UTF-8 only |
| 006_rewrite | Heap rewrite (VACUUM FULL / CLUSTER) not implemented |
| 007_ddl | DDL replication out of scope |
| 008_diff_schema | Schema divergence between publisher and subscriber |
| 009_matviews | Materialized views not replicated |
| 010_truncate | TRUNCATE replication out of scope |
| 011_generated | Generated columns |
| 012_collation | Non-deterministic collations |
| 013_partition | Partitioned tables |
| 014_binary | Binary wire format (pgoutput binary mode) |
| 015–019 | pgoutput v2 streaming of large/in-progress transactions |
| 020_messages | Transactional decoding messages (pg_logical_emit_message) |
| 021–023 | Two-phase commit (PREPARE TRANSACTION) |
| 024_add_drop_pub | ALTER SUBSCRIPTION ADD/DROP PUBLICATION |
| 025_rep_changes_for_schema | Schema publications (FOR TABLES IN SCHEMA) |
| 027_nosuperuser | Non-superuser replication permissions |
| 028_row_filter | Row filters on publications |
| 029_on_error | skip_on_error / error handling modes |
| 030_origin | replication origin tracking |
| 031_column_list | Column list publications |
| 032_subscribe_use_index | Subscriber index selection hints |
| 033_run_as_table_owner | Table-owner permission enforcement |
| 034_temporal | Temporal tables |
| 035_conflicts | Multiple unique conflicts |
| 100_bugs | Bug regressions (multiple features) |

## CSV Updates

For each ported test, add a row to `docs/test-port/postgres-oracle-port-status.csv`:

```
S-001,postgres/src/test/subscription/t/001_rep_changes.pl,tap,port,yes,Ported as TestPort_Subscription001RepChanges in internal/testport/subscription_port_test.go,-
S-004,postgres/src/test/subscription/t/004_sync.pl,tap,port,yes,Ported as TestPort_Subscription004Sync,-
S-026,postgres/src/test/subscription/t/026_stats.pl,tap,port,yes,Ported as TestPort_Subscription026Stats,-
```

Update D-004 row: add `deferred_to=M0094` (subset ported); remaining tests still
deferred at the suite level.

## PostgreSQL Reference

- `postgres/src/test/subscription/t/` — upstream test sources.
- `postgres/src/backend/replication/logical/worker.c` — apply worker,
  `apply_handle_insert`, `apply_handle_update`, `apply_handle_delete`.
- `postgres/src/backend/replication/logical/tablesync.c` — initial table sync
  worker, `srsubstate` state machine.
- `postgres/src/backend/catalog/pg_subscription.h` — catalog shapes.
