# M0103-0007 rung 4 — PG-publisher → goopg-subscriber REPLICA IDENTITY FULL

## Status

accepted

## Context

Rungs 1–3 of M0103-0007 exercised the apply path under the default
replica-identity rules:

  - **REPLICA IDENTITY DEFAULT** (the implicit setting for tables with
    a primary key). pgoutput's `logicalrep_write_update` writes the
    old-tuple section only when key columns were modified; otherwise
    the byte after `rel_oid` is `'N'` directly. Rung 2 closed the
    apply-worker's silent-drop on this no-old-tuple shape by
    synthesising the row-locator key from the new tuple's PK columns
    via `primaryKeyOnlyRow`.

That covered the most common publisher configuration but left two
apply-side branches untested:

  1. The `len(m.OldTuple) > 0` branch of `applyUpdate` — i.e., the
     publisher sent an explicit old-tuple section. Under REPLICA
     IDENTITY DEFAULT this only happens on key-column UPDATEs, which
     the rung-1/2/3 tests never exercised.
  2. The `decodePgoutputTupleAsRow` call against a tuple where every
     column has status `'t'` (text) instead of a mix of `'t'`/`'n'`/`'u'`.
     Rung 2's no-old-tuple path skips this entirely; the synthesised
     key is built from already-decoded new-tuple values.

REPLICA IDENTITY FULL exercises both. Under FULL, pgoutput emits
`'O' | full pre-image tuple` before `'N' | new tuple` on every UPDATE,
and `'O' | full pre-image tuple` on every DELETE — regardless of
whether key columns changed. The apply worker's full-row equality
match in `rowMatchesKey` then has to find the heap row whose every
column equals the pre-image, not just the PK columns.

The canonical use case for REPLICA IDENTITY FULL is **a table without
a primary key**. Under DEFAULT a no-PK table can't be the source of
UPDATE/DELETE in a publication — upstream rejects with `cannot update
table … because it does not have a replica identity and publishes
updates`. FULL is the v0-compatible way to publish UPDATE/DELETE from
no-PK relations, and the apply side has to handle them by sequential
scan (no PK index to probe).

## Decision

Add `TestPort_PgoutputInteropPGToGoopgReplicaIdentityFull` adjacent to
the rung-3 `TestPort_PgoutputInteropPGToGoopgBatchDML`. Same harness
shape (`pubsubcluster.NewMixed` PG-pub + goopg-sub, pre-created
logical slot, `(enabled=true, copy_data=false, slot_name=<pre>,
create_slot=false)` CREATE SUBSCRIPTION). Differences:

  - **No primary key**. The publisher and subscriber both declare
    `CREATE TABLE public.t (a int, v text)` (no PK constraint). The
    apply worker's `primaryKeyOnlyRow` returns nil for this table, so
    rung 2's no-old-tuple synthesis path is unreachable — every
    UPDATE/DELETE must travel through the `len(m.OldTuple) > 0`
    branch.
  - **REPLICA IDENTITY FULL on the publisher**:
    `ALTER TABLE public.t REPLICA IDENTITY FULL` issued before the
    subscription is created. Every UPDATE/DELETE will then carry an
    `'O'` block containing the full pre-image of the row.
  - **Workload**: three INSERTs (`(1,'a')`, `(2,'b')`, `(3,'c')`),
    one no-key-touched UPDATE (`SET v='bb' WHERE a=2`), one DELETE
    (`WHERE a=1`). Small enough to keep the test fast; broad enough to
    exercise all three apply paths.

### What this pins (the apply paths exercised)

  - **applyInsert** — verifies that the no-unique-index path through
    `maintainUniqueIndexesForInsert` is a no-op (the helper bails out
    of the `!idx.Unique && !idx.Primary` filter and inserts nothing).
    The row reaches the heap and becomes visible to fresh-session
    SeqScans.
  - **applyUpdate's `len(m.OldTuple) > 0` branch** — `'O'` triggers
    `decodePgoutputTupleAsRow(m.OldTuple)`, building a full Row where
    every cell carries a value (no NULL skip-cells). `rowMatchesKey`
    then does full-column equality on the heap, finds the pre-image,
    and `applyUpdateByKey` deletes/re-inserts.
  - **applyDelete** — `'O'` triggers the same decode path; the
    `rowMatchesKey` full-row match locates the row by sequential scan,
    sets `xmax`, and emits the heap-delete WAL record.

### Assertions

Each `psc.WaitForRow` opens a fresh `database/sql` connection so
visibility is established per-statement, not per-cached-plan.
Predicates are written so they don't require a PK index — fresh-
session SeqScan suffices.

  - `SELECT count(*) FROM public.t` → 2 (3 inserted, 1 deleted).
  - `SELECT count(*) FROM public.t WHERE a = 2 AND v = 'bb'` → 1
    (the UPDATE's new state).
  - `SELECT count(*) FROM public.t WHERE a = 1` → 0 (the DELETE).
  - `SELECT count(*) FROM public.t WHERE a = 3 AND v = 'c'` → 1
    (untouched row keeps its original value).

## Verification

```
go test -count=1 -timeout 120s \
  -run TestPort_PgoutputInteropPGToGoopgReplicaIdentityFull \
  ./internal/testport/
```

→ PASS. Confirms the apply worker's full-old-tuple decode and
sequential-scan match paths work end-to-end against an upstream PG
publisher under REPLICA IDENTITY FULL.

## Out of scope

  - **TOAST columns** (`'u'` status code). The pre-image of a row
    whose TOASTed column wasn't modified carries `'u'` instead of a
    `'t'`-typed payload; `decodePgoutputTupleAsRow` currently errors
    on `'u'`. TOAST support lands as its own rung.
  - **`pgbench` workload**. Sustained-workload coverage at the rung-3
    scale already exists; rung-4 stays small to keep the test fast and
    the surface focused.
  - **`kill -9` on PG + libpq multi-host reconnect**. Slated for a
    later rung once the workload-side coverage is complete.
  - **DDL replication** (`CREATE TABLE`, `ALTER TABLE` propagation).
    pgoutput does not propagate DDL in any PG version; the schema must
    exist on both ends. The harness already handles this; only the
    `ALTER TABLE … REPLICA IDENTITY FULL` step is new, and it runs on
    the publisher side only.
