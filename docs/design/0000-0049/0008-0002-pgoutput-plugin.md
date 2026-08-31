# `pgoutput` Output Plugin (Milestone 0008)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted                                               |
| Date       | 2026-04-29                                             |
| Milestone  | 0008 — Logical Replication Support                     |
| Refines    | [0008-0001-logical-decoding-pipeline.md](0008-0001-logical-decoding-pipeline.md) |
| Supersedes | —                                                      |

## Problem

`0008-0001` delivered the logical-decoding pipeline framework: slots,
reorder buffer, decoder, classifier, per-slot loop, and the
slot-creation-time HISTORIC catalog snapshot. The `OutputPlugin`
interface is in place but has no implementation — `SlotDecoder.Run`
needs a real plugin to drive.

`pgoutput` is upstream PostgreSQL's built-in logical-replication
output plugin and the one every subscriber expects. Wire-compatible
emission lets goopg's apply worker (0008-0004) consume goopg's
publisher output by reading the same byte format upstream's
walsender produces. This document covers v0's pgoutput shape.

## Decision

### Wire format — protocol v1, text format

Match upstream's `pgoutput` v1 byte layout for the messages M0008
covers (`B` / `C` / `R` / `I` / `D`). Streaming-of-in-progress
transactions (v2's `S` / `Y` / `c` / `A`), TRUNCATE (`T`), MESSAGE
(`M`), TYPE (`Y`), ORIGIN (`O`), 2PC (`b` / `P` / `K` / `r`), and
`UPDATE` (`U`) are all **out of scope** for this loop.

(`U` is excluded only from this first slice because v0's executor
emits UPDATE as a paired HeapDelete + HeapInsert in the WAL, so the
decoder hands the plugin two separate `Change` events. Re-folding
them into a single `U` message requires a reorder-buffer pass that
isn't justified by the M0008 DoD; the apply worker reconciles the
pair.)

All multi-byte integers are big-endian to match upstream's
`pq_sendint*` family. Reference: `postgres/src/backend/replication/
logical/proto.c`.

#### Message shapes

```
'B' Begin              kind(1) | final_lsn(8) | commit_time(8) | xid(4)
'C' Commit             kind(1) | flags(1)=0   | commit_lsn(8) | end_lsn(8) | commit_time(8)
'R' Relation           kind(1) | rel_oid(4) | nspname\0 | relname\0
                         | replident(1) | nattrs(2)
                         | per-attr: flags(1) | name\0 | type_oid(4) | typmod(4)
'I' Insert             kind(1) | rel_oid(4) | 'N' | tuple
'D' Delete             kind(1) | rel_oid(4) | 'K' | tuple
```

Tuple body (`logicalrep_write_tuple`):

```
nliveatts(2)
per attr: status(1)
  status='n' (NULL) — no payload
  status='u' (unchanged TOAST) — no payload (not used in v0)
  status='t' (text)  — len(4) | bytes(len)
```

xid in `I` / `D` is omitted in protocol v1 (only emitted in v2
streaming mode); we never emit it.

#### Replica identity (`'R'.replident` byte)

| Byte | Meaning                |
| ---- | ---------------------- |
| `d`  | DEFAULT (primary key)  |
| `f`  | FULL (whole row)       |
| `n`  | NOTHING                |
| `i`  | INDEX                  |

v0 emits `d` for every relation: the planner / catalog don't yet
distinguish replica-identity modes, and the M0008 milestone DoD
requires only that `DEFAULT` round-trip cleanly. `FULL` / `NOTHING`
/ `INDEX` semantics ride on the existing replica-identity catalog
work tracked under the publication / subscription DDL slice
(`0008-0003`). The M0008 DoD `relreplident=NOTHING → UPDATE/DELETE
fails at the publisher with a clear error` is enforced there, not
in the encoder.

### Implementation: `internal/wal/pgoutput.go`

`PgOutput` implements the existing `OutputPlugin` interface
(`Begin` / `Change` / `Commit`):

- Construction: `NewPgOutput(snap *CatalogSnapshot, w io.Writer)`.
  Snapshot is the slot-creation-time HISTORIC view from
  `0008-0001-loop-6`. `w` is whatever the per-slot framing path
  hands the plugin — typically a `bytes.Buffer` in tests and a
  `CopyDataMessage` writer in production.
- Emission state: a `map[uint32]struct{}` of already-emitted
  relation OIDs. The first `Change` for a given relation in a
  session emits `R` ahead of the `I` / `D`; subsequent changes
  skip `R`. Mirrors upstream's `relsynced` set.
- Tuple body encoding: walks the column descriptors from the
  snapshot, parses the heap-tuple body via the same
  null-flag-then-value frame `internal/executor/codec.go::DecodeRow`
  uses, and renders each value to upstream's text format. v0
  supports the column types HammerDB / pgbench / TPC-H exercise:
  `int4`, `int8`, `text` / `varchar`, `numeric`, `bool`,
  `timestamp` / `date`. Unknown types emit `'t'` with the raw
  bytes — a transparent passthrough that lets the apply worker at
  least recognise the column position.

### Why duplicate the column decoder?

`internal/executor/codec.go::DecodeRow` already implements the
exact body format pgoutput needs to decode. But `internal/wal`
must not import `internal/executor` — that direction inverts the
dependency tree (executor depends on wal already, transitively
through storage). So `internal/wal/pgoutput.go` carries a small,
self-contained tuple-body reader marked clearly as "mirror of
executor/codec.go::DecodeRow"; if the format ever drifts, both
sides break together by virtue of the shared format invariant
documented at the top of codec.go.

The duplication is < 80 lines, no new types, no behavioural
divergence — a worse alternative would be to extract a tiny
shared package, which we'd grow into anyway once the type-system
milestone normalises the on-disk shape. Until then duplication
is the cleaner of the two costs.

### Wiring into the slot decoder

`SlotDecoder.Snapshot.Catalog` (already plumbed in 0008-0001 loop 6)
is the snapshot `NewPgOutput` consumes. Production startup will
call something like:

```go
snap := wal.SlotSnapshot{Catalog: wal.BuildCatalogSnapshot(catalog)}
plugin := wal.NewPgOutput(snap.Catalog, copyDataWriter)
dec, _ := wal.NewSlotDecoderWithSnapshot(slots, name, w, walDir, segSize, plugin, snap)
go dec.Run(ctx)
```

That wire-up lives in the START_REPLICATION handler when 0008-0003
lands; this loop ships only the encoder and its unit tests.

### What this loop doesn't deliver

- `'U'` UPDATE message — paired HeapDelete + HeapInsert path
  through the executor; deferred.
- `'T'` TRUNCATE — milestone-out-of-scope.
- `'Y'` TYPE — only needed when subscribers see a non-built-in
  type column; deferred.
- `'M'` MESSAGE — operator-issued messages; deferred.
- 2PC messages — milestone-out-of-scope.
- Streaming (v2) framing — milestone-out-of-scope.
- Replica identity beyond `d` (DEFAULT) — needs catalog support
  in 0008-0003.
- Binary format (`binary=on` subscriptions) — milestone-out-of-scope.

## Verification

`internal/wal/pgoutput_test.go`:

- `TestPgOutputBeginEmitsCanonicalShape` — `Begin(xid=42,
  commitLSN=0x100)` produces a 21-byte message starting `'B'`,
  with `final_lsn`/`commit_time`/`xid` in the right positions.
- `TestPgOutputCommitEmitsCanonicalShape` — `Commit(xid, lsn)`
  produces the 26-byte `'C' | 0 | commit | end | time` shape.
- `TestPgOutputInsertEmitsRelationOnceThenInsert` — first
  Change(Insert) for a relation emits `R` then `I`; the second
  Change against the same relation emits only `I`.
- `TestPgOutputInsertEncodesIntAndText` — given an `int4` +
  `text` table, the tuple body decodes the on-disk null flags
  and value bytes and re-emits them in the upstream-text
  format.
- `TestPgOutputDeleteEmitsKMarker` — `Change{Kind: ChangeDelete}`
  produces `'D' | rel_oid | 'K' | tuple` (v0 emits a 0-attr
  tuple body because the existing HeapDelete record carries
  no pre-image; the apply worker resolves the row by `(rel,
  block, slot)` lookup).

## Cross-references

- Milestone: `docs/milestones/0008-logical-replication-support.md`.
- Pipeline foundation: `0008-0001-logical-decoding-pipeline.md`.
- Sibling design (DDL): `0008-0003-publication-subscription-ddl.md`
  (planned).
- Upstream:
  - `postgres/src/backend/replication/logical/proto.c` — every
    `logicalrep_write_*` shape this implementation mirrors.
  - `postgres/src/include/replication/logicalproto.h` — message
    kind byte constants.
  - `postgres/src/backend/replication/pgoutput/pgoutput.c` —
    the upstream output plugin.
