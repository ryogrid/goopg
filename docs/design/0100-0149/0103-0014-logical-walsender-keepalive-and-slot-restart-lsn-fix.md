# 0103-0014 — Logical walsender keepalive + slot RestartLSN off-by-one

**Status:** accepted (2026-05-14)
**Owner:** M0103-0008 (rung 9)
**Closes the rung-9 stream-stability gap surfaced after rung 8 (0103-0013).**

## Context

After rung 8 (`CREATE_REPLICATION_SLOT` parenthesised options list) landed,
PG's `CREATE SUBSCRIPTION g2pg_sub … WITH (enabled = true, copy_data = false)`
against a goopg publisher succeeded. The live interop test
(`TestPort_PgoutputInteropGoopgToPG`) then advanced to `START_REPLICATION SLOT
g2pg_sub LOGICAL 0/0 (proto_version '4', publication_names 'p')`, but the
test timed out at exactly 60 seconds with PG reporting:

```
ERROR:  terminating logical replication worker due to timeout
```

That is `wal_receiver_timeout` (default 60 s). The PG apply worker received
**no** `'w'` or `'k'` CopyData frame during the 60 s window, even though the
publisher had executed `INSERT/INSERT/UPDATE/DELETE` against the published
table.

Adding short-lived diagnostic logging into `runLogicalWalsender`'s
`dec.Run` goroutine revealed two distinct defects:

1. **Decoder crash on first read** —
   `wal: slot "g2pg_sub" decoder iterator: wal: invalid record header:
   unknown rmid=240`. The iterator's very first `readOneAt(pos)` decoded
   garbage as an XLogRecord header. `dec.Run` returned the error to the
   walsender, which then sat idle until PG's timeout fired.
2. **No keepalive emission** — even if the decoder had stayed alive, the
   LOGICAL walsender had no keepalive loop. The physical-walsender path
   (`replyStartReplication`) runs a 10 s `time.Ticker` that emits
   `protocol.EncodeKeepalive` frames; `runLogicalWalsender` had no
   symmetric path. A quiet publisher would hit `wal_receiver_timeout`
   even when the decoder was healthy.

Both gaps must close for rung 9. Without (1) the stream errors out the
first time the decoder runs; without (2) the stream times out the next
quiet 60 s after that.

## Root cause for the decoder crash

`replyCreateReplicationSlot` set the new slot's `RestartLSN` to the
publisher's current `WrittenLSN()`:

```go
var startLSN uint64
if s.cfg.WAL != nil {
    startLSN = s.cfg.WAL.WrittenLSN()
}
slot, err := s.cfg.Slots.Create(name, slotKind, startLSN)
```

`WrittenLSN()` returns the **LSN of the last byte appended**, *not* the LSN
where the next record will start. `wal.NewRecordIterator` then computes
`pos := startLSN - 1`, which lands on the *last byte of the previous
record* rather than the first byte of the next record. The very first
`readOneAt` decodes garbage from inside the previous record's payload —
the leading byte happens to fall outside `MaxKnownRmgr=11`, so the
header validation in `wal.DecodeXLogRecordHeader` fires
`unknown rmid=240` (or whatever random byte the payload held).

This is the same off-by-one M0094-0005 closed for `startStandbyReplayer`
/ `startWalreceiver`: the standby's tail anchor must be `WrittenLSN()+1`,
the next record's first-byte LSN, not `WrittenLSN()` which placed the
iterator inside the last record and crashed the replayer with
`bad xlog total length 0`. Replication slots use the identical iterator
abstraction with the identical contract.

## Changes

### Slot creation: `+1` to anchor at next record

`internal/server/replication.go::replyCreateReplicationSlot`:

```go
var startLSN uint64
if s.cfg.WAL != nil {
    startLSN = s.cfg.WAL.WrittenLSN() + 1
}
slot, err := s.cfg.Slots.Create(name, slotKind, startLSN)
```

The comment quotes the contract (`first byte of the next record, not the
last byte of the current one`) and cross-references M0094-0005 so future
readers see the precedent. Both `SlotPhysical` and `SlotLogical` use the
same code path, so the fix benefits both kinds; PHYSICAL replication
previously hid the bug because `replyStartReplication` reads from
`args.StartLSN` (client-supplied, already `next-byte`-aligned by libpqrcv)
rather than `slot.RestartLSN`. LOGICAL is the first consumer that reads
the slot's stored anchor.

### Keepalive emission in `runLogicalWalsender`

`internal/server/logicalwalsender.go`:

- New method `walsenderPgoutputAdapter.WriteKeepalive(sendTime
  time.Time) error`. Takes the adapter's mutex (so it never races with
  an in-flight `'w'` frame), advertises `walEnd = nextLSN - 1` (the
  last-emitted synthetic LSN; underflow-safe when no frame has been
  shipped), wraps `protocol.EncodeKeepalive(walEnd, sendTime, false)`
  in a `'w'`-style CopyData frame, flushes through the FrameWriter.
- New goroutine in `runLogicalWalsender` that fires
  `adapter.WriteKeepalive` every 10 s. The cadence matches the physical
  path. Lifecycle: started after `dec.Run`'s goroutine, watches
  `streamCtx.Done()` for shutdown, propagates write errors via
  `streamCancel`. The main `select` drains `keepaliveDone` after
  `receiveDone` so the connection-close ordering is symmetric with
  the physical path.

### Regression tests

`internal/server/logicalwalsender_test.go`:

- `TestWalsenderPgoutputAdapterKeepalive` — pins that
  `WriteKeepalive` emits a parseable `'k'` frame, advertises
  `WALEnd = last-emitted synthetic LSN`, and sets `ReplyRequested=false`
  (idle keepalive, not a force-reply).
- `TestWalsenderPgoutputAdapterKeepaliveBeforeFirstWrite` — pins
  the no-messages-yet underflow guard (adapter with `nextLSN=0`
  must still emit a well-formed keepalive with `WALEnd=0`).

`internal/server/replication_test.go`:

- `TestReplicationCreateLogicalSlotRestartLSNIsNextRecord` — appends
  a record so `WrittenLSN()` is non-zero, creates a logical slot via
  the wire protocol, asserts `slot.RestartLSN == WrittenLSN() + 1`.
  Quotes the rmid=240 garbage-decode mechanism in the doc comment so a
  future reader doesn't lose the why behind the `+1`.

## Why this is a rung boundary

The failure mode changed observably:

- **Before** the fixes: PG fired
  `ERROR: terminating logical replication worker due to timeout` at
  exactly 60 s, and the goopg walsender's log showed
  `decoder exit ... unknown rmid=240` on every `START_REPLICATION`
  attempt.
- **After** the fixes: PG's apply worker stays alive past the 60 s
  mark; the test still fails the `WaitForRow ... id=2 AND v='updated'`
  assertion because no pgoutput message reaches PG yet, but the
  connection itself is stable. The 60 s timeout is no longer the
  presenting symptom — the next rung (rung 10) is "pgoutput emission
  for goopg-publisher DML" rather than "stream stability".

That clean fault-line separates rung 9 (stream stability) from rung 10
(actual DML propagation) and matches the rung-by-rung structure M0103-0008
has used since rung 1.

## Out of scope (rung 10 next)

With rungs 1–9 closed, the next gap surfaces:

> Connection stays alive for 67+ seconds (until test shutdown) but PG
> sees zero rows from the publisher's `INSERT/INSERT/UPDATE/DELETE`. The
> SlotDecoder runs without errors, the iterator blocks at tail, but no
> `'w'` frame carrying pgoutput Begin/Relation/Insert is shipped to the
> subscriber.

Candidate causes for rung 10: publication-filter rejection (the
`buildPublicationFilter` may not match `public.t` registered via the
harness), missing `Begin/Commit` emission for in-snapshot transactions,
or the catalog snapshot taken at session start not seeing the published
table because of catalog timing. Each is a separate diagnostic step; the
rung-10 design doc will land alongside the next fix.

## Verification

`go test -race -count=1 -timeout 60s -run "TestReplicationCreateLogicalSlot|TestWalsenderPgoutputAdapter" -v ./internal/server/`
→ all 8 tests PASS, including the three new regression pins.

`go test -race -count=1 -timeout 300s ./internal/server/ ./internal/wal/
./internal/executor/ ./internal/catalog/ ./internal/parser/
./internal/planner/ ./internal/analyzer/` → all green.
