# 0103-0023 — M0103-0008 Scenario B closure (goopg primary + PG subscriber)

Status: ACCEPTED 2026-05-14 (loop 19)

## Context

M0103-0008's `TestE2E_LogicalFailoverGoopgToPG` (Scenario B) drives a
goopg publisher with a PG subscriber. The umbrella probe-survival
ladder for the libpqrcv (`CREATE SUBSCRIPTION` apply-launcher) surface
landed across 16 rungs in loops 1–18:

- rungs 1–5: parser / planner / executor support for the
  `fetch_table_list` SRF shape
  (VARIADIC, `(srf(...)).*`, `array_agg`, `ProjectSet`,
  derived-subquery composite expansion).
- rungs 6–7: LATERAL FROM-clause SRF arg resolution
  (planner-side `lateralCtx` threading + executor-side
  `BindLateralOuter` slot binding).
- rung 8: `CREATE_REPLICATION_SLOT` parenthesised options list.
- rung 9: logical walsender keepalive + slot.RestartLSN off-by-one.
- rungs 10–11: publication-table canonicalisation and
  `publication_names` SplitIdentifierString quoting.
- rung 12: classifier coverage for `HeapHotUpdate` / `HeapUpdate` +
  fresh-page-INSERT logical/FPI coexistence.
- rung 13: LATERAL `pg_catalog.pg_get_publication_tables(...)` parser
  dispatch.
- rung 14: `pg_class.relnatts` column.
- rung 15: `pg_get_publication_tables.relid` matches `pg_class.oid`.
- rung 16: `pg_class.oid` numeric OID + `relreplident` column +
  catalog-aware `::regclass` cast.

After rung 16, the live probe surfaced no new failure mode — the test
passed end-to-end on first try.

## What this loop did

Lifted the `t.Skip` on `TestPort_PgoutputInteropGoopgToPG` and ran the
live end-to-end Scenario-B test against an upstream PG 18.3 subscriber.

Observed behaviour:
- goopg publisher cluster starts, accepts upstream PG client
  connections via libpq.
- `CREATE TABLE` on both sides; `CREATE PUBLICATION p FOR TABLE t` on
  publisher; `CREATE SUBSCRIPTION g2pg_sub … WITH (enabled=true,
  copy_data=false)` on subscriber.
- libpqrcv ladder runs cleanly:
  - `fetch_table_list` returns `(public, t, NULL)`.
  - `fetch_remote_table_info` returns `relreplident='d'`,
    `pg_class.oid` numeric.
  - column-types LATERAL probe over `pg_attribute` /
    `pg_get_replica_identity_index` resolves — goopg's
    pg_attribute view already exposes `attnum`, `attname`,
    `atttypid`, `attisdropped`, `attgenerated`; the
    `pg_get_replica_identity_index` builtin returns 0
    (`InvalidOid`), so `i.indexrelid = 0 LEFT JOIN` falls through
    and the probe consumes the row with `attnum != ANY(i.indkey)`
    = false and `attgenerated != ''` = false, both of which match
    upstream's default-replica-identity semantics.
- The apply worker launches and `pg_subscription_rel` populates with
  exactly one row for `public.t`.
- Publisher runs `INSERT(1, hello)`, `INSERT(2, world)`,
  `UPDATE … SET v='updated' WHERE id=2`, `DELETE WHERE id=1`. WAL
  emission goes through the rung-12 logical+FPI path; pgoutput
  Begin/Relation/Insert/Update/Delete/Commit frames ship over the
  walsender to the apply worker.
- Subscriber reaches the expected final state
  `(id=2, v='updated')` within ~10 ms of the publisher's DML
  statements. `pg_replication_origin_status.remote_lsn` advances
  to a non-zero LSN.

Verified stable across 5 consecutive runs (1.6–1.8 s each, including
PG cluster bring-up and tear-down).

## Changes in this loop

- `internal/testport/pgoutput_interop_test.go`:
  removed the `t.Skip("M0103-0008 rung 16 closed … rung 17 deferred")`
  guard. Replaced the rung-17-OPEN comment block with a rung-17-CLOSED
  closure note pointing at this design doc. Test body is unchanged
  from rung-16 — it was already a strict end-to-end pass condition.

## Why no fix was needed for rung 17

The ladder's working hypothesis (rung-17 = `pg_attribute` /
`pg_get_replica_identity_index` column-types probe) turned out to be
already satisfied by prior work. Specifically:

- goopg's virtual `pg_attribute` view (registered alongside
  `pg_class` in `internal/catalog/catalog.go::registerSystemTables`)
  already exposes the columns the probe selects.
- `pg_get_replica_identity_index(oid) → oid` is registered as a
  pure-Go builtin returning 0 when the relation has no explicit
  replica-identity index — which matches every goopg table in v0
  (`relreplident='d'` everywhere, no `REPLICA IDENTITY USING INDEX`
  support). The LEFT JOIN's `i.indexrelid = 0` predicate then makes
  every `pg_index` row drop out, leaving NULL columns from `i.*`
  which the outer `attnum = ANY(i.indkey)` evaluates as NULL,
  coerced to false. Functionally equivalent to upstream PG's
  REPLICA IDENTITY DEFAULT behaviour.

The rung-16 catalog flip (numeric pg_class.oid, decimal-text
encoding, `relreplident` column) was the final missing piece — every
later probe in the ladder pivots off that one column shape.

## Tests

The full end-to-end pin is `TestPort_PgoutputInteropGoopgToPG` in
`internal/testport/pgoutput_interop_test.go` — runs to completion
without `t.Skip`, asserts the four-DML final-state outcome:

- `count(*) FROM public.t WHERE id = 2 AND v = 'updated'` = 1
- `count(*) FROM public.t WHERE id = 1` = 0
- `count(*) FROM public.t` = 1

The unit-level rung pins from loops 2–18 remain in place across
parser / planner / analyzer / executor / catalog / server / wal /
storage packages.

## Verification

Loop 19:

```
go test -count=1 -timeout 60s -run TestPort_PgoutputInteropGoopgToPG \
  ./internal/testport/
# PASS, 1.79 s (consistent across 5 consecutive runs)

go test -race -count=1 -timeout 300s \
  ./internal/parser/ ./internal/planner/ ./internal/analyzer/ \
  ./internal/executor/ ./internal/server/ ./internal/wal/ \
  ./internal/catalog/ ./internal/storage/
# all green (recorded at commit time)
```

## What this closes

- M0103-0008 (Scenario B E2E test: goopg primary + PG subscriber).
- The umbrella probe-survival ladder for libpqrcv interop with a
  goopg publisher.

## Next step in M0103

M0103-0009 — close milestone.
