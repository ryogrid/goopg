# M0103-0007 rung 3 — PG-publisher → goopg-subscriber batch DML

## Status

accepted

## Context

M0103-0007 rungs 1 and 2 closed the silent-drop bugs in the apply
worker's INSERT/UPDATE paths and pinned a single-row INSERT / INSERT /
UPDATE / DELETE round-trip from a PG publisher to a goopg subscriber
(`TestPort_PgoutputInteropPGToGoopgFullDML`). The note at the end of
rung 2 deferred the next step:

> The full Scenario A failover wiring (pgbench, kill -9, libpq
> multi-host reconnect) remains the principal remaining work and will
> be sequenced as further rungs, each with its own design doc + pin
> per the M0103-0008 closure protocol.

`pgbench` runs a continuous stream of single-row INSERT/UPDATE/DELETE
xacts against the publisher. Before wiring the failover plumbing
(kill -9 + libpq multi-host reconnect), the next gap to close is
**sustained-workload correctness**: the apply worker must survive
*many* single-row xacts in sequence — many pgoutput Begin/Commit
boundaries, many heap pages spanning page-image emission for first-
dirty-in-epoch (the rung 12/14 path on the M0103-0008 side), many
fresh-session visibility probes — without silently dropping rows.

The rung-2 test exercises exactly four statements over one heap
page. A pgbench session does ~hundreds per second over many pages.
A scale-verification rung between these two regimes is the natural
next step.

## Decision

Add `TestPort_PgoutputInteropPGToGoopgBatchDML` adjacent to the
rung-2 `TestPort_PgoutputInteropPGToGoopgFullDML`. Same harness shape
(`pubsubcluster.NewMixed` with PG publisher + goopg subscriber,
pre-created logical slot, `(enabled=true, copy_data=false,
slot_name=<pre>, create_slot=false)` CREATE SUBSCRIPTION); larger
workload:

  - **Phase 1 — 50 INSERTs**: ids 1..50, `v='row-N'`. Each statement
    is its own xact, so the apply worker sees 50 distinct
    Begin/Insert/Commit triples. Crosses heap-page boundaries, which
    on the publisher side emits `RecordKindPageImage` records on the
    first-dirty-in-epoch boundaries (the rung-14 emission shape from
    the M0103-0008 side); the apply worker only consumes pgoutput
    Insert frames, so all 50 must appear.
  - **Phase 2 — 25 UPDATEs**: rewrites `v` on ids 1..25, no key
    column touched. Hits the rung-2 no-old-tuple synthesis path
    (`primaryKeyOnlyRow`) 25 times in a row.
  - **Phase 3 — 10 DELETEs**: ids 41..50 (a distinct range from the
    UPDATE range). Exercises the apply-side DELETE path against rows
    that have only ever seen an INSERT — verifies the orphan-PK-entry
    behaviour from rung 1 scales beyond a single row.

Assertions read the subscriber from fresh database/sql sessions
(each `psc.WaitForRow` reconnects), so every check runs through the
goopg PK IndexScan path:

  - Total surviving row count = `surviving = inserted - deleted = 40`.
  - Each updated id has the rewritten `v='updated-N'`.
  - Each non-updated, non-deleted id keeps its original `v='row-N'`.
  - Each deleted id is gone — heap re-fetch + MVCC dead-tuple
    filtering drops the orphan PK entry.

## Verification

`go test -count=1 -timeout 160s
-run TestPort_PgoutputInteropPGToGoopgBatchDML ./internal/testport/`
→ PASS (~2.1 s). Confirms the rung-1/2 fixes scale to 50× the rung-2
workload without false-positive visibility, dropped UPDATEs, or
stale DELETE artefacts.

## Out of scope

  - pgbench itself. The next rung will either (a) wire pgbench
    against the PG publisher with `pgbench_history` polling, or (b)
    introduce a single-statement multi-row INSERT path
    (`INSERT INTO t VALUES (1,a),(2,b),…`) that compresses many
    pgoutput Insert messages into a single xact — depending on
    which surface surfaces a gap first.
  - `kill -9` on PG + libpq multi-host reconnect on the client side.
    Slated as a later rung once the workload-side rungs land.
  - REPLICA IDENTITY FULL / TOAST / DDL replication. Each will land
    as its own rung with its own design doc + pin per the M0103-0008
    closure protocol.
