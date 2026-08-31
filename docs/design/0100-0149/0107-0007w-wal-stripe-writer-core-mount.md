# 0107-0007w — Mount `*stripeWriterCore` on `Writer` (slice B call-site rewrite part 1)

**Status**: accepted (2026-05-21)

**Milestone**: M0107-0007 (Phase D4 — WAL insert striping). Slice B
call-site rewrite, part 1 of N. Parent:
`docs/design/perf-optimize/07-wal-fsm-insert.md` §2.

Foundations 1–15 ([[0107-0007h]] / [[0107-0007i]] / [[0107-0007j]] /
[[0107-0007k]] / [[0107-0007l]] / [[0107-0007m]] / [[0107-0007n]] /
[[0107-0007o]] / [[0107-0007p]] / [[0107-0007q]] / [[0107-0007r]] /
[[0107-0007s]] / [[0107-0007t]] / [[0107-0007u]] / [[0107-0007v]])
landed all the slice B primitives plus the [[0107-0007v]]
`stripeWriterCore` packaging struct. This part lands the first call-
site rewrite step: mounting the core as a field on `Writer` and
instantiating it in `NewWriter`. The mounted core remains dead code
for production WAL flow — `state.append` continues to drive the
legacy path — but the field now exists where the subsequent rewrite
parts (replacing `state.append`'s body with `s.core.Append(procNum,
encoded)` and the drain prelude with `s.core.PublishUpTo(...)`)
expect it.

## Problem

[[0107-0007v]] §"Why this is dead code" enumerated the rewrite's
site footprint:

- `Writer` gains one field: `core *stripeWriterCore`.
- `NewWriter` adds one constructor call after `walBuf` / `memRing`
  are built.
- `state.append`'s body switches to `s.core.Append(procNum, encoded)`.
- The drain goroutine calls `s.core.PublishUpTo(...)` before
  `readForDrain` / `writeAt` / `advanceHead`.

The four items are independent edits but they have an order
dependency: items 3 and 4 are byte-emission-changing call-site
rewrites that need the PG-compat WAL byte-diff integration gate from
the parent milestone (see M0107-0007 `Verification:` block).
Items 1 and 2 are pure structural plumbing — they do NOT change byte
emission because nothing yet calls into the mounted core. Landing
them in their own loop:

- Pins the mount-point so the next loops are mechanical body
  rewrites against an established field name.
- Exercises the constructor's `walBuf` / `memRing` borrowing under
  the real WAL test fixtures (the [[0107-0007v]] tests use direct
  `newStripeWriterCore` calls; this loop pins the wiring through
  `NewWriter`).
- Keeps the diff for the byte-emitting rewrites focused on body
  changes, not field-introduction noise.

The foundation-first precedent for splitting a rewrite into a "mount
the consumer" part followed by call-site body parts is slice C
([[0107-0007e]] `selectFSMCandidatePage` packaged the slice C
foundations before [[0107-0007f]] / [[0107-0007g]] rewrote the
call sites that consumed it).

## Solution

`internal/wal/writer.go` gains one field on `Writer`:

```go
core *stripeWriterCore
```

and `NewWriter` constructs it after `loadState` returns:

```go
w.core = newStripeWriterCore(
    uint64(cfg.SegmentSize),
    uint64(st.writePos),
    st.prevRecPtr,
    st.walBuf,
    st.memRing,
)
```

Construction is unconditional. `loadState` always returns a non-nil
`*state`, so the four constructor inputs are always well-defined:

- `cfg.SegmentSize` is normalised to non-zero by `cfg.withDefaults`
  (so `newInsertPosTracker`'s `segSize > 0` invariant holds).
- `st.writePos` is the recovery-resume LSN from
  `detectWritePos` — `0` for a fresh cluster, the last record's end
  byte position otherwise.
- `st.prevRecPtr` is the upstream-style 0-based RecPtr of the last
  appended record; `0` for a fresh cluster.
- `st.walBuf` is `nil` when `Config.WALBuffers == 0`; the core
  borrows it as-is and per-foundation nil-safety propagates.
- `st.memRing` is non-nil even at `SenderMemoryBuffer == 0`
  (`NewMemRing(0)` returns nil in code review; see "Ring nil-safety"
  below).

The constructor's cross-segment hook closure
(`emitSegmentPad`-on-error-panic) is installed inside
`newStripeWriterCore`; this site does not duplicate that wiring.

## Ring nil-safety

Three deployment matrices the mount must survive:

| WALBuffers | SenderMemoryBuffer | walBuf | memRing |
|---|---|---|---|
| 0 | 0 | nil | nil |
| >0 | 0 | non-nil | nil |
| 0 | >0 | nil | non-nil |
| >0 | >0 | non-nil | non-nil |

Each composing foundation is independently nil-safe — the writer
chain ([[0107-0007u]] `stripeAppend`) and the drain chain
([[0107-0007t]] `publishVisibility`) both branch on nil rings and
skip the corresponding ring-side step without altering the in-memory
tracker state. Mounting the core unconditionally is therefore safe
in every matrix.

`TestStripeWriterCoreMountedAcceptsBareConfig` pins the
`WALBuffers=0, SenderMemoryBuffer=0` corner — the most aggressive
nil-ring case — through the full `NewWriter` constructor path.

## Why this is still dead code

Mounting the field does not invoke `c.Append` or `c.PublishUpTo`.
`state.append` continues to drive the legacy single-mutex insert
path; `drainBufferBytes` continues to drive the legacy drain prelude.
No production goroutine reaches the core's methods. The mount-point
only becomes hot under the subsequent call-site rewrites (slice B
call-site rewrite parts 2/3 in future loops).

This is the same pattern as [[0107-0007a]] (heap-extend lock striping
landed as a slice A foundation before [[0107-0007f]] / [[0107-0007g]]
turned slice C foundations into the FSM-pin-aware heap-insert call
site).

## Lock-ordering tier

Unchanged from [[0107-0007v]] §"Lock-ordering tier". The mount-
point does not run either chain; the chain definitions become live
only when parts 2/3 rewrite the call sites.

## PG-compat

None. This change:

- Adds an in-memory struct field on a Go-side type (`Writer`).
- Adds one constructor call in `NewWriter`.
- Does not touch WAL record framing, CRC, page header, block
  reference frames, the catalog, or the wire protocol.
- Does not change byte emission — `state.append` and `drainBufferBytes`
  still produce exactly the same bytes as before.

The PG-compat WAL byte-diff gate from M0107-0007's verification
block fires when parts 2/3 rewrite the call sites that actually
emit bytes through the core. This part is below that gate.

## Tests

Two regression tests in
`internal/wal/stripe_writer_core_mount_test.go`:

- `TestStripeWriterCoreMountedAfterNewWriter` —
  `NewWriter(WALBuffers=64KiB, SenderMemoryBuffer=64KiB)` produces
  a `Writer` whose `core` is non-nil with:
  - `core.memRing == w.memRing` (same allocation, two consumers)
  - `core.walBuf == w.stateRef.walBuf` (same allocation, two
    consumers)
  - `core.Load() == (uint64(writePos), prevRecPtr)` — recovery-
    resume contract holds on construction
  - `core.locks` / `core.posTracker` / `core.inserting` /
    `core.publisher` all non-nil (constructor invariant)
- `TestStripeWriterCoreMountedAcceptsBareConfig` —
  `NewWriter(WALBuffers=0, SenderMemoryBuffer=0)` still produces a
  non-nil core with both rings nil; `core.Load() == (0, 0)` and
  `core.PublishedTail() == 0` for an empty WAL dir.

The ten existing `TestStripeWriterCore*` tests in
`stripe_writer_core_test.go` continue to exercise the core's method
contracts via direct construction; this loop adds end-to-end mount
coverage through the production `NewWriter` path.

Verified: `go test -race -count=1 -run 'TestStripeWriterCore'
./internal/wal/` PASS (1.03 s); `go test -race -count=1
./internal/wal/` PASS (3.18 s).

## Out of scope (future call-site rewrite parts)

- Part 2 — replace `state.append`'s body with
  `s.core.Append(procNum, encoded)`. Lifts `appendMu`'s
  writePos / writeLSN invariants into `insertPosTracker`'s `posMu`
  and per-stripe local state. Requires the PG-compat WAL byte-diff
  gate.
- Part 3 — replace the drain goroutine's per-tick prelude with
  `s.core.PublishUpTo(...)`. Lifts `appendMu`'s walBuf / memRing
  invariants into the publication-walker watermark. Requires
  drain-vs-stripe concurrent execution (drain must run without
  blocking stripe writers).
- 8-byte MAXALIGN of record sizes in the Append pre-amble so
  reservation gaps are always multiples of 8 (avoids the
  [[0107-0007s]] `padLen < 24` and `padLen == 25` corner cases).
- Decision on dead-code-removing [[0107-0007h]] `lsnAllocator` once
  the call-site converges on the `insertPosTracker` +
  `insertionTracker` + `tailPublisher` trio — that cleanup belongs
  to the rewrite's final part.

## References

- Parent design: `docs/design/perf-optimize/07-wal-fsm-insert.md` §2
  (WAL Insert striping).
- Foundation packaging: [[0107-0007v]] (`stripeWriterCore` struct,
  constructor, method contracts).
- PG counterpart: `postgres/src/backend/access/transam/xlog.c` —
  `XLogInsertRecord`'s call chain runs against a process-global
  `XLogCtl` plus per-backend insert state. goopg's `Writer.core`
  packages the equivalent insert state on the writer object instead
  of a process global; the field's lifetime matches the Writer's.
- Foundation-first precedent (slice C): [[0107-0007e]]
  `selectFSMCandidatePage` packaged the slice C foundations before
  [[0107-0007f]] / [[0107-0007g]] consumed them at the heap-insert
  call site.
