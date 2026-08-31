# 0005-0008 — Promotion drain must stop at end-of-WAL, not at byte parity

status: accepted
supersedes step 4 of: [0005-0005 Standby Promotion](0005-0005-promotion.md)
related: [0005-0002 Standby recovery and replay](0005-0002-standby-recovery-and-replay.md)

## The failure

Nightly `AI-20260812-005501-001` (`TestE2E_FailoverPGtoGoopg/async`):

```
goopg promote: ERR promote: drain timed out after 5s (apply_lsn=50347104, target=50347312)
```

208 bytes short, after waiting the full `drainTimeout`. The other two
subtests (`sync_on`, `sync_remote_apply`) passed in the same run, and the
whole test passes on most runs — it reads as flakiness.

It is not flakiness. It is an unreachable stop condition that most runs
happen to satisfy by luck.

## Why byte parity is the wrong condition

`standbyController.runPromote` (0005-0005 step 3/4) snapshots
`WAL.WrittenLSN()` as the drain target and polls
`StreamReplayer.ApplyLSN()` until it reaches that value. The two
quantities are not commensurable:

- `WrittenLSN` is a **byte position** — the last byte the walreceiver
  appended. The receiver appends the primary's stream *verbatim*
  (`walreceiver.go` `appendVerbatim` → `Writer.AppendRaw`).
- `ApplyLSN` is a **record boundary** — `rec.EndLSN` of the last record
  the replayer applied.

A walsender cuts its stream at whatever byte offset its send buffer and
flush position land on; nothing aligns that cut to a record boundary. So
whenever the receiver's last frame ends mid-record, the received tail is
a partially-transmitted record that can never be applied, `ApplyLSN`
stops one record short, and the gap — 208 bytes in the nightly — is
permanent. The 5 s wait cannot close it; it only delays the error.

Trailing page padding that no record covers produces the same shape.

The test passes whenever the cut happens to land on a record boundary,
which is common (a stream that ends just after a commit flush usually
does), hence the intermittency.

## What upstream does

PostgreSQL never asks for byte parity. `xlogrecovery.c`'s `ReadRecord`
treats a short or zeroed record at the tail as **end of WAL**, not as an
error, and recovery finishes at the last complete record — that
`EndRecPtr` is what promotion anchors on. goopg's `finalizePromotion`
already anchors its timeline switch at `ApplyLSN()`, i.e. it already
agrees with upstream about *where* the old timeline ends; only the drain
loop's stop condition disagreed.

## The fix

Introduce an explicit end-of-WAL signal and drain to it.

**`RecordIterator.AtEndOfWAL()`** reports that the cursor cannot advance
because the stream holds no further complete record. It is backed by a
`parkedAtEnd` flag published while `Next` is blocked, set from the
classification at the block site:

| block site | meaning | `parkedAtEnd` |
|---|---|---|
| cursor at tail (`pos >= WrittenLSN`) | everything consumed | true |
| `readOneAt` → bare `ErrLSNNotWritten` | record runs past `WrittenLSN`; never received | true |
| `readOneAt` → `errWALBytesUndrained` | bytes exist inside `WrittenLSN`, momentarily neither buffered nor drained | **false** |

The third row is the reason the classification lives at the read site
rather than in the observer. An earlier attempt gated `AtEndOfWAL` on
`DrainedLSN() >= WrittenLSN()` instead; that is wrong in both directions
— a writer that has not been asked to flush can sit with
`DrainedLSN == 0` indefinitely (the new unit test reproduces exactly
that), so end-of-WAL would never be reported. `errWALBytesUndrained`
wraps `ErrLSNNotWritten`, so every existing `errors.Is` check keeps
matching and only the new classification sees the difference.

**`StreamReplayer.AtEndOfWAL()`** forwards to the iterator its `Run`
call is draining (nil before `Run` → false).

**`runPromote`** breaks out of the drain loop on
`applied >= target || replayer.AtEndOfWAL()`, logging the shortfall.
`drainTimeout` stays: it now covers only a genuine apply stall, which is
what its comment always claimed it covered.

### Correctness precondition

`AtEndOfWAL` answers "cannot advance **right now**". That is only
equivalent to "will never advance" if nothing can append. `runPromote`
establishes exactly that before the drain loop: it cancels the
walreceiver and waits on `receiverDone` (0005-0005 steps 1–2, already
mandatory for the target snapshot to be meaningful). The precondition is
documented on both methods; no other caller should use them.

## Verification

- `TestStreamReplayerEndOfWALOnPartialTail` (`internal/wal`) — applies a
  whole record, appends a second record truncated 8 bytes short of its
  `TotLen`, and asserts `AtEndOfWAL()` goes true while `ApplyLSN` stays
  on the last complete record. Non-vacuity: it asserts
  `ApplyLSN < WrittenLSN` at that moment, i.e. that the old byte-parity
  rule was genuinely unreachable — if the gap ever closed, the test
  would stop testing anything. Proven fail-when-broken (stubbing
  `AtEndOfWAL` to false reproduces the 3 s hang and the failure text).
- `internal/wal` full suite, `cmd/goopg`, `internal/server`;
  `-race` over the iterator/replayer tests.
- `TestE2E_FailoverPGtoGoopg` (all three subtests),
  `TestE2E_NativeOnlyReplicationAndPromotion`,
  `TestE2E_GoopgColdStartOnPGDataDir`.

## Deferred

The partial record is left in place at the tail of the old timeline.
Upstream overwrites such a tail with an *overwrite-contrecord* record
(`xlog.c` `CreateOverwriteContrecordRecord`) so that a later reader does
not trip on it; goopg's new primary simply continues appending after
those bytes. Recorded in `.ralph/deferral_ledger.md` — the promotion
itself is correct because the timeline history anchors at `ApplyLSN`,
but a reader that walks the old timeline's raw bytes past the switch
point would still meet the fragment.
