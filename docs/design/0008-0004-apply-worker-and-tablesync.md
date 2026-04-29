# Apply Worker and Table Sync (Milestone 0008)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted                                               |
| Date       | 2026-04-29                                             |
| Milestone  | 0008 — Logical Replication Support                     |
| Refines    | [0008-0001-logical-decoding-pipeline.md](0008-0001-logical-decoding-pipeline.md), [0008-0002-pgoutput-plugin.md](0008-0002-pgoutput-plugin.md), [0008-0003-publication-subscription-ddl.md](0008-0003-publication-subscription-ddl.md) |
| Supersedes | —                                                      |

## Problem

Loops 0008-0001 through 0008-0003 deliver the publisher side of the
logical-replication pipeline: slots, decoder, classifier, pgoutput
encoder, plus the publication / subscription catalog substrate and SQL
surface. The subscriber side is still missing:

- A worker process that connects to the publisher's logical slot.
- A pgoutput **decoder** that turns the wire bytes back into change
  events (the inverse of the 0008-0002 encoder).
- An `apply` loop that opens a local transaction at `B`, applies each
  row-level event to the corresponding local table, and commits at `C`.
- An initial table-sync state machine (`pg_subscription_rel.srsubstate`
  letters: `i` / `d` / `s` / `r`) that copies a table's current
  contents before streaming begins.
- Restart-safety: resume from the last `confirmed_flush_lsn` after a
  clean stop, a publisher disconnect, or a crash on either side.

This document covers the milestone's apply worker design plus the v0
implementation scope. The full DoD list lives in M0008's
acceptance bar — this loop ships the foundation that subsequent loops
plug into.

## Decision

### Architecture

```
publisher (goopg)                              subscriber (goopg)
─────────────────                              ──────────────────
walsender ── CopyData(WAL frames) ──▶          tcp/libpq pgoutput stream
   ▲                                                      │
   │ runs SlotDecoder.Run                                 ▼
   │ → wal.PgOutput emits B/C/R/I/D                pgoutput.Decoder
                                                           │
                                                           ▼
                                                       ApplyWorker
                                                  (one per subscription)
                                                           │
                                                           ▼
                                                  local goopg storage
                                                  (heap / catalog)
```

One apply worker per active subscription. Single goroutine consuming
the byte stream sequentially — no parallel apply.

### `pgoutput.Decoder`: inverse of 0008-0002's encoder

`internal/wal/pgoutput.go::PgOutput` already encodes pgoutput v1 byte
shapes. This loop adds `internal/wal/pgoutput_decoder.go` with:

```go
type DecodedMessage struct {
    Kind      byte                // pgoBegin / pgoCommit / pgoRelation / pgoInsert / pgoDelete
    XID       storage.TransactionID
    CommitLSN uint64
    EndLSN    uint64
    Relation  *DecodedRelation    // populated for `R`
    RelOID    uint32              // populated for `I` / `D`
    NewTuple  []DecodedColumn     // populated for `I`
    OldTuple  []DecodedColumn     // populated for `D`
}

type DecodedRelation struct {
    OID       uint32
    Schema    string
    Name      string
    Replident byte
    Columns   []DecodedAttr
}

type DecodedAttr struct {
    Flags     byte
    Name      string
    TypeOID   uint32
    TypeMod   int32
}

type DecodedColumn struct {
    Status byte    // 'n' (NULL), 't' (text), 'u' (unchanged TOAST)
    Bytes  []byte  // text bytes when Status == 't'
}

func DecodeMessage(payload []byte) (*DecodedMessage, error)
```

`DecodeMessage` reads the kind byte, dispatches to the per-kind
parser, and returns a typed event. The format matches upstream PG's
`logicalrep_read_*` family in `proto.c`.

### `ApplyWorker` skeleton

`internal/server/applyworker.go` provides:

```go
type ApplyWorker struct {
    sub      *catalog.Subscription
    pubsub   *catalog.PubSub
    catalog  catalog.Catalog
    pool     *storage.Pool
    txnMgr   *mvcc.Manager

    // relations is the subscriber's running cache of `R`
    // messages. Keyed by remote rel_oid. UPDATE / DELETE
    // resolve their target table via this map.
    relations map[uint32]*wal.DecodedRelation

    // currentTxn is the local transaction opened on `B` and
    // committed on `C`.
    currentTxn mvcc.Transaction
    inXact     bool
}

func NewApplyWorker(...) *ApplyWorker

// ApplyMessage drives one decoded pgoutput event into local
// storage. Returns the remote LSN to advance confirmed_flush_lsn
// to (the commit record's EndLSN) when the event is `C`, else 0.
func (w *ApplyWorker) ApplyMessage(m *wal.DecodedMessage) (uint64, error)
```

`Run(ctx, source io.Reader)` is the byte-stream driver: read one
pgoutput message at a time, decode, apply, repeat. The TCP wiring
that produces `source` lands in the next M0008 loop alongside the
slot-start handshake; this loop ships the in-process apply path so
tests can drive it from the existing `PgOutput` encoder output.

### Per-event behaviour

| Event       | Apply step                                                                              |
| ----------- | --------------------------------------------------------------------------------------- |
| `B` Begin   | `txnMgr.Begin(ReadCommitted)`; remember xid + commit LSN.                              |
| `R` Relation | Cache the decoded relation under its remote OID. Resolve a local `*catalog.Table` by `(schema, name)` lookup once and store. |
| `I` Insert  | For each text column, parse to a `Datum` per the column's catalog.Type. Encode + insert via `storage.PageAddHeapTuple`. |
| `D` Delete  | v0's pgoutput emits an empty `K` body; the apply worker treats this as a no-op on the first slice. Real `(rel, block, slot)` resolution lands in the next loop with the wire-format extension that carries the pre-image. |
| `C` Commit  | `txnMgr.Commit(currentTxn)`. Return the commit LSN so the caller advances `confirmed_flush_lsn`. |

### Tablesync state machine — deferred

The `pg_subscription_rel.srsubstate` letters (`i` initial, `d` data
copy, `s` synchronized, `r` ready) require a separate per-table
worker that COPYs the publisher's current contents before joining
the streaming apply. v0's first slice doesn't ship this; the apply
worker assumes the local table is empty at slot start (the empty-
table case the TestReplicationEndToEnd harness relies on). Tablesync
lands as a follow-up loop alongside the slot-start handshake.

### Restart safety

When the apply worker (re)starts, the subscription's slot already
carries the last `confirmed_flush_lsn` that the publisher's
`SlotDecoder.Run` advanced (M0008 / 0008-0001 loop 5). The
subscriber resumes the slot from that LSN; the publisher's WAL
classifier replays everything past it. No subscriber-side state
file is needed for the streaming case — `pg_subscription_rel`'s
`srsublsn` covers the per-table tablesync handoff in the future
loop.

### Conflict handling

Per the M0008 milestone Out of Scope: "Conflict resolution beyond
stop-on-error." A duplicate-key INSERT or a missing-target
UPDATE/DELETE surfaces as an error from the storage layer; the apply
worker logs it and stops (no automatic retry). Operators reset by
adjusting state and restarting the worker.

### What this loop doesn't deliver

- TCP transport for the pgoutput stream — the apply worker takes an
  `io.Reader` so tests can drive it from the encoder's `bytes.Buffer`
  output. Real slot-start handshake (`START_REPLICATION SLOT name
  LOGICAL ...`) and the message-framing-from-CopyData unwrap follow
  in the next loop.
- Initial COPY transport for tablesync — the catalog state
  machine for `pg_subscription_rel.srsubstate` (`i` / `d` /
  `s` / `r`) landed in a separate slice (see "Tablesync state
  machine — catalog substrate" below), but no worker yet
  walks a relation through those transitions.
- UPDATE message support — encoder doesn't emit `U` yet.
- DELETE row resolution — emit-time pre-image is missing; first
  slice no-ops on `D`.

## Verification

`internal/server/applyworker_test.go` drives the apply worker
against a byte stream produced by the existing `wal.PgOutput`
encoder:

- `TestApplyWorkerInsertsRowFromPgoutputStream`: publisher-side
  produces a `B → R → I → C` sequence for a one-column int4
  table; the subscriber-side worker decodes the stream and the
  local table contains the inserted row at the end. Pins the
  encoder/decoder symmetry plus the per-event apply contract.
- `TestApplyWorkerCommitAdvancesLSN`: confirms the commit
  return value matches the encoder's commit LSN argument so
  the caller can advance `confirmed_flush_lsn`.
- `TestPgoutputDecoderRoundTrip`: pure decoder unit test —
  encode B/C/R/I/D, decode each message, confirm round-trip.

## Tablesync state machine — catalog substrate

Upstream tracks per-(subscription, relation) sync progress in
`pg_subscription_rel.srsubstate`. The column is a single
character that walks monotonically from `i` (init) through
`d` (data copying) and `s` (sync done — caught up to a known
LSN) to `r` (ready — fully integrated into the streaming
apply). A reversal would imply rewinding a synced table back
to copy mode, which would silently re-apply rows; the catalog
must reject it.

`internal/catalog/pubsub.go` carries the state machine. Each
row is a `SubscriptionRel { SubID, RelOID, State, LSN }`,
indexed by subscription name + relation OID:

```
subRels map[string]map[uint32]*SubscriptionRel
```

Constants `SubRelStateInit="i"`, `SubRelStateDataCopy="d"`,
`SubRelStateSyncDone="s"`, `SubRelStateReady="r"` are the
only legal values; an unknown character returns
`ErrInvalidSubRelState`. The validity table

```
i → {i, d, s}     // i→s shortcut covers tables empty at slot start
d → {d, s}
s → {s, r}
r → {r}
```

is enforced by `AdvanceSubscriptionRel(subName, relOID,
newState, newLSN)`; an illegal jump (e.g. `i → r`) or a
reversal (`r → d`) returns `ErrInvalidSubRelStateTransition`.
LSN updates use `max(old, new)` so a stale path that supplies
a smaller LSN cannot rewind sync progress.

`AddSubscriptionRel` seeds a fresh row at state `i` and
returns `ErrSubscriptionRelExists` on duplicates;
`DropSubscription` clears the entire `subRels[name]` bucket
so a re-CREATE under the same name doesn't inherit stale
state. Read accessors: `LookupSubscriptionRel(subName,
relOID)`, `SubscriptionRels(subName)` (per-subscription
slice), and `AllSubscriptionRels()` (cross-subscription
slice — used by the catalog view).

`internal/initdb/replication_views.go` wires the
`pg_subscription_rel` virtual view to render rows from
`AllSubscriptionRels()`: `srsubid` and `srrelid` come from
the row, `srsubstate` is the state character verbatim, and
`srsublsn` is `formatLSN(LSN)` (or empty when LSN is zero).
The view is no longer "always zero rows" — once a worker
drives the state machine, `\d+ pg_subscription_rel` will
reflect tablesync progress.

What the substrate doesn't deliver: the actual COPY
transport that reads each table's snapshot, and the
apply-worker hook that calls `AdvanceSubscriptionRel`
during streaming. Both land in the next two M0008 slices.

## Cross-references

- Pipeline foundation: `0008-0001-logical-decoding-pipeline.md`.
- Encoder: `0008-0002-pgoutput-plugin.md`.
- Catalog substrate: `0008-0003-publication-subscription-ddl.md`.
- Upstream:
  - `postgres/src/backend/replication/logical/proto.c` —
    `logicalrep_read_*` family (the inverse the decoder mirrors).
  - `postgres/src/backend/replication/logical/worker.c` and
    `tablesync.c` — apply worker + per-table sync worker.
