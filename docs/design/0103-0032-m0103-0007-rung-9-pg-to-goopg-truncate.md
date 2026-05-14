# M0103-0007 rung 9 — PG-publisher → goopg-subscriber TRUNCATE replication

## Status

accepted

## Context

Rungs 1–8 (designs `0103-0023` through `0103-0031`) drove every
INSERT/UPDATE/DELETE shape across pgoutput proto_version=1 and
verified the apply worker handles each correctly. What none of them
touched is `TRUNCATE`, which pgoutput emits as its own message kind:

> Upstream's `logicalrep_write_truncate` writes the literal byte `'T'`
> followed by a 4-byte big-endian relation count, a 1-byte option
> bitmask (bit 0 = `TRUNCATE_CASCADE`, bit 1 = `TRUNCATE_RESTART_SEQS`),
> and one 4-byte relid per relation in the statement.

The goopg apply worker before this rung dispatched only on the six
DML-shape kinds (`B/R/I/D/U/C`); any other kind fell through to the
`default` arm of `ApplyMessage` and returned the typed error
`"applyworker: unsupported pgoutput kind %q"`. A real publisher
running `TRUNCATE TABLE t` on a published relation would crash the
subscriber's apply loop with that error and stall the slot at the
crash LSN. This rung closes that gap end-to-end:

  - Decoder gains a `case pgoTruncate` arm in
    `internal/wal/pgoutput_decoder.go::DecodeMessage` and two new
    fields on `DecodedMessage`: `TruncateRels []uint32` and
    `TruncateOption byte`.
  - `ApplyWorker.ApplyMessage` gains a `case 'T'` branch routing to
    a new `applyTruncate(*wal.DecodedMessage)` method.
  - `applyTruncate` walks the relid list, resolves each via the
    existing relation cache (same cache the I/D/U paths use), and
    calls the existing `truncateRelation(ctx, rel)` primitive that
    stamps `xmax = currentTx.XID` on every visible tuple in the
    heap. The work is transactional with the surrounding apply xact:
    a rollback before COMMIT discards the marks via MVCC, symmetric
    with `applyDeleteByKey`.

The load-bearing correctness property:

> When the publisher commits a `TRUNCATE t1, t2, …` statement, the
> subscriber's post-apply state shows zero rows in each named
> relation; fresh-snapshot SeqScans return empty.

## Decision

### Wire-format support

The decoder additions are mechanical mirrors of
`logicalrep_write_truncate` / `logicalrep_read_truncate` in
`postgres/src/backend/replication/logical/proto.c`. Two `wal`-package
constants encode the option bitmask:

```
pgoTruncateCascade        = 0x01
pgoTruncateRestartSeqs    = 0x02
```

`DecodedMessage.TruncateOption` records the byte verbatim so callers
can route on either bit if needed. `DecodedMessage.TruncateRels` is a
freshly-allocated `[]uint32` per frame; the empty-list case is treated
as a no-op rather than rejected (the publisher never emits it in
practice but a defensive decoder costs nothing).

### Apply-path semantics

`applyTruncate` reuses `truncateRelation` from
`internal/executor/operators_ddl.go`. The function:

  1. Pins each heap page in `[0, NBlocks(rel))`.
  2. For each line pointer, decodes the heap tuple header.
  3. If `mvcc.TupleVisibleSubxact(header, ctx.Snap, ctx.Tx.XID, …)`
     returns true, calls `storage.PageSetHeapTupleXmax(page, slot,
     ctx.Tx.XID)`.

This is the same xmax-stamping path `applyDeleteByKey` uses; the only
difference is that TRUNCATE stamps every visible tuple rather than
the single matching row. Subsequent fresh-snapshot SeqScans see the
stamps in the commit log and treat the tuples as dead.

This is **soft** truncate — the heap file is not physically shrunk.
Goopg's SQL TRUNCATE (`execTruncate`) does call
`Pool.Manager().TruncateRelation(rel)` for the physical shrink path,
but that primitive is non-transactional: a crash between truncate and
the apply-worker commit would leave the subscriber in a state where
truncate took effect but the surrounding pgoutput xact rolled back —
durability divergence from the publisher. Stamping xmax keeps the
apply xact atomic at the cost of leaving dead bytes on disk until the
next vacuum. For the M0103 scope (logical-failover correctness over
short test windows) this is the right trade.

### Unknown-relid policy

`applyTruncate` rejects relids that have no prior `R` message in the
slot — same policy as `applyDelete` and `applyUpdate`. A drift between
the publisher's and subscriber's catalogs (subscriber dropped the
table, publisher republished a new OID for the same name, etc.) is a
real correctness hazard; silently no-oping would mask it. The error
surfaces through `ApplyMessage`'s shared error path and is logged with
`event=apply_error`, `kind=T`, `rel_oid=<offending OID>` by the apply
worker.

### Option-byte handling

Both option bits are publisher-side decisions:

  - `pgoTruncateCascade` — the publisher already expanded the FK
    closure into the relid list before emitting `'T'`, so the
    subscriber sees the full set of affected relations and needs no
    extra work to honour CASCADE semantics.
  - `pgoTruncateRestartSeqs` — goopg has no sequence-state model in
    the apply path (sequence catalog is not currently replicated),
    so RESTART IDENTITY is a no-op on the subscriber side.

The byte is recorded on `DecodedMessage.TruncateOption` so future
rungs that gain sequence replication can inspect it without changing
the decoder shape.

## Tests

Unit pins:

  - `TestPgoutputDecoderTruncateMessage` in
    `internal/wal/pgoutput_decoder_test.go` — four sub-tests build
    `'T'` payloads by hand (no encoder helper) and verify decoder
    fields: single relid, multi-relid with CASCADE+RESTART option
    bits, empty relid list, and truncated payload after the option
    byte.
  - `TestApplyWorkerTruncate` in
    `internal/executor/applyworker_test.go` — drives Begin → Relation
    → Insert(x2) → Truncate → Commit through `ApplyMessage` directly
    against an in-process subscriber catalog + storage fixture;
    asserts a fresh-snapshot SeqScan returns zero rows.
  - `TestApplyWorkerTruncateUnknownRelOid` — pins the
    catalog-drift rejection: a `'T'` for an OID with no prior `'R'`
    must return an error.

Live E2E pin:

  - `TestPort_PgoutputInteropPGToGoopgTruncate` in
    `internal/testport/pgoutput_interop_test.go` — full publisher
    (upstream PG) + subscriber (goopg) wiring. Workload: INSERT three
    rows, TRUNCATE the published table, INSERT two more rows. After
    apply convergence the subscriber sees only the two post-truncate
    rows (fresh `database/sql` sessions via `psc.WaitForRow`). Two
    fail-fast assertions: `count(*) = 2` catches a TRUNCATE no-op (3
    or 5 rows would slip through); per-id identity checks catch the
    inverse (TRUNCATE wiping post-truncate inserts as well).

## Verification

```
go test -count=1 -timeout 60s -run "TestPgoutputDecoderTruncateMessage" ./internal/wal/
go test -count=1 -timeout 60s -run "TestApplyWorkerTruncate|TestApplyWorkerTruncateUnknownRelOid" ./internal/executor/
go test -count=1 -timeout 120s -run "TestPort_PgoutputInteropPGToGoopgTruncate" ./internal/testport/
```

Race-tested regression on `./internal/executor/ ./internal/wal/
./internal/server/ ./internal/catalog/ ./internal/testutil/pubsubcluster/`
all green.

## Next rungs (deferred within M0103-0007 scope)

  - pgbench against PG publisher with `pgbench_history` polling
    (real-workload load-test).
  - proto_version=2 streaming subxacts (`Y`/`A` frames + parent-XID
    linkage on the apply side).
  - `kill -9 <pg-pid>` + libpq multi-host reconnect plumbing on the
    client side — completes the DoD assertion shape.
