M0132-S7 landed: proc-slot discipline — the `(procNum + halfSize) %
ConnSlotCount` offset is retired. Out-of-block extended `Execute` now begins on
the connection's OWN slot (`dispatch_extended.go`), matching `dispatch.go:236`;
`copy.go` uses the own slot out-of-block and the manager's auto-assign path
in-block/nil (COPY-ignores-block divergence ledger'd).

Root cause: the offset is a bijection onto the connection region, so it
deterministically landed the autocommit transaction on a DIFFERENT connection's
own slot, where `Begin(iso, procNum)` unconditionally `inTxn.Store(1)` — the
"two live transactions on one slot" condition behind doc 09 §5 I3's
`mvcc: unknown transaction` aborts. Fix pin: `TestM0132S7_ExtendedAutocommitUsesOwnSlot`
(red at HEAD: "reserved transaction on slot 482 was clobbered … mvcc: unknown
transaction"; green after). Design doc `docs/design/0132-0003-extended-txn-proc-slot-discipline.md`.

Gates run this loop:
- `go test ./internal/server/ ./internal/mvcc/` PASS (40s).
- `go test -race ./internal/server/ ./internal/mvcc/` PASS (125s) — milestone bar item 10.
- `pgbench -T 30 -c 50 -j 8 -S -M prepared` → 2,448,195 txns, **0 failed**, 0
  "unknown transaction" in server log, TPS 81,610. (Throwaway capped instance,
  port 5545; the wrapper script's final boolean printed a spurious "GATE FAIL"
  from a grep -c/echo quoting bug — the measured numbers above are the real result.)

In-flight: none.

Next per the `## Current Priority` banner: **M0132-S8 — mixed simple↔extended
blocks (PRIMARY slice)**. S7 removed the offset-slot collision that was the
leading confound for S8's "two live transactions per connection" case. S8 scope:
(a) ONE live tx per connection (extended Execute must not allocate a second — now
structurally impossible after S7); (b) COMMIT/ROLLBACK on either protocol
finalises work on the other; (c) status byte coherent across the switch; (d) an
in-block error on one protocol aborts statements on the other; (e) a D-002
isolation spec + a `lib/pq` driver-level test. Gates: D-002 spec + driver test.
