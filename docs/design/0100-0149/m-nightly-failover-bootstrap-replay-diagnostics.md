# M-NIGHTLY AI-20261021-010544-001 — a pg→goopg standby that received a row but never showed it

Status: diagnostic landed 2026-10-21; root cause found and fixed 2026-10-21 (§6) — closed.

## 1. Failure

On the 2026-10-21 nightly (run 010544, sha e64b1bfe9),
`TestE2E_FailoverPGtoGoopg/async` failed at its bootstrap probe:

- the PG primary inserts `(-999, 'bootstrap')` right after the goopg standby
  reaches `streaming`;
- the standby's `SELECT count(*) … WHERE client = -999` stayed 0 for 30 s.

The failure carried:

- `pg_stat_wal_receiver` = `streaming 0/30201C8 0/30201C8`;
- a bench\_log heap file of 0 bytes;
- a log tail of probe connections only.

The sync\_remote\_apply and sync\_on arms passed. The test failed at 01:13,
while the nightly's race lane was saturating the host.

## 2. What was measured

- **Reproduction.** The async arm passes on its own: 5/5 at about 9.5 s, and
  6/6 at 2× CPU oversubscription.
- **Where the row sits.** On a passing run, the setup up to the bootstrap row
  is deterministic: initdb, CREATE TABLE, a physical slot, pg\_basebackup.
  The row's commit ends at `0/3003100` (`pg_current_wal_lsn()` right after
  the INSERT). The failed standby had received and flushed up to
  `0/30201C8`, about 116 KiB further.
- **Conclusion.** The row was shipped and received, so the defect is on
  goopg's side, between receive and visibility:
  - replay stopped or lags (StreamReplayer);
  - the commit's `ReplayXactCommit` never ran;
  - the tuple was replayed but is not visible.

  The 0-byte heap file does not tell these apart: a replayed page can sit in
  the buffer pool unflushed.
- **Side finding.** On every passing run, the primary's
  `pg_stat_replication` shows empty write/flush/replay. PG's walreceiver
  replies whenever its write or flush position advances
  (`XLogWalRcvSendReply`). goopg's waits for `wal_receiver_status_interval`
  (10 s). This is ledgered.

## 3. Change

**The functions.** `pg_last_wal_receive_lsn()` and
`pg_last_wal_replay_lsn()` (xlogfuncs.c; pg\_proc 3820/3821) raised 42883.

- They now read `Context.RecoveryLSNs`, which cmd/goopg wires from
  `standbyController.recoveryLSNs` through `postmaster.Config` (and into
  parallel workers):
  - receive is the walreceiver's received LSN. The highest value seen is
    kept, as PG keeps `WalRcv->flushedUpto` after its walreceiver exits;
  - replay is `StreamReplayer.ApplyLSN()`, the end of the last replayed
    record (PG's `lastReplayedEndRecPtr`).
- A 0 position is NULL. That covers a primary, which has no hook, and a
  standby before streaming starts.

**The test diagnostic.** `waitForGoopgCount` takes optional extra diagnostic
functions. The bootstrap wait passes `walProgressDiagnostics`, which reports:

- `want_lsn` (the primary's LSN right after the row's commit);
- the primary's current LSN and every walsender's sent/write/flush/replay;
- the standby's receive LSN, replay LSN and `pg_is_in_recovery()`.

The next failure therefore says which of these happened:

- `sent < want_lsn`: the row was not shipped;
- `receive >= want_lsn > replay`: replay is stuck;
- `replay >= want_lsn`: the row was replayed but is invisible, which points
  at ReplayXactCommit / the snapshot.

A passing run shows `want_lsn=0/3003100`, `sent=0/3003100` and
`standby_receive_replay_recovery=[0/3003100 0/3003100 t]`.

## 4. Pins

- `TestPgLastWALLSN` (executor): NULL without positions or before
  streaming, the formatted LSN otherwise.
- The E2E family stays green:
  - TestE2E\_FailoverPGtoGoopg, all three modes;
  - TestE2E\_FailoverGoopgToPG;
  - TestE2E\_PGStandbyFullCycle;
  - TestE2E\_NativeOnlyReplicationAndPromotion.

## 5. Not done (ledgered)

- The root cause: it waits for the next occurrence and its LSN notes.
- `pg_last_wal_replay_lsn` on a primary that ran crash recovery: PG reports
  where recovery ended, goopg returns NULL (its crash recovery is not the
  StreamReplayer).
- The walreceiver's reply cadence (§2).
- `pg_last_xact_replay_timestamp` is still unimplemented.

## 6. Root cause and fix (2026-10-21, closed)

**Reproduction.** `TestE2E_PGStandbyFullCycle`'s goopg reverse standby
failed 1 run in 4 with the same symptom. Its log showed `walreceiver WALData
start mismatch start_lsn=50331648 expected_start_lsn=50463176`. The
expected position, `0x030201C8`, is exactly where the nightly's standby was
stuck. A temporary stack probe on `Writer.Append`, active while in standby
mode, showed every goopg-local append coming from the walreceiver itself.

**Cause 1: the XLogData header was misread.** The second field of a 'w'
message is the sender's current end of WAL ("The current end of WAL on the
server"; `XLogSendPhysical` sends `SendRqstPtr`), not the end of this
message's data. A PG primary catching a standby up caps each frame at
MAX\_SEND\_SIZE (128 kB) and reports a walEnd past it.

`handleCopyData` appended a frame verbatim only when `walEnd ==
start + len`. Otherwise it called `Writer.Append`, framing the primary's raw
bytes as a new goopg record. During catch-up the local WAL therefore:

- diverged from the primary's, growing by a header and padding per frame;
- never replayed those bytes as the primary's records;
- discarded every later frame below the drifted position as already
  present.

The committed row in both failures came after such a stretch, so it never
reached the standby.

**Cause 2, masked by cause 1.** The receiver requested `START_REPLICATION`
at `WrittenLSN + 1`, goopg's record-start convention. A PG walsender reads
that as a byte position and began one byte past the local end, so every
frame was a gap. Before the fix above, the first such frame was wrapped as a
record and hid the gap.

**Fix:**

- Every non-empty XLogData frame is raw stream bytes, appended at StartLSN
  through StartLSN+len; any overlap with local WAL is trimmed. The
  record-wrapping branch is gone.
- The receiver requests the start of the segment holding its next byte,
  `streamingStartLSN`, as PG's `RequestXLogStreaming` does ("We always
  start at the beginning of the segment"). A segment boundary is the same
  byte to both walsenders: goopg's `NewRecordIterator` reads a boundary as
  itself. The primary's re-sent prefix is trimmed on receipt.
- goopg's walsender now sends PG's walEnd: `max(chunk end, FlushedLSN)`.
  The libpq field is documented as the sender's end of WAL.

**Measured:**

- The replication E2E family, 16 tests (PG→goopg failover in all modes,
  goopg→PG failover, the full cycle, physical replication, recovery ports),
  passed in 4 of 4 repeated runs. Before the fix the full cycle failed 1 in 4.
- Each standby now logs exactly one start mismatch: the segment-start
  prefix trim.
- With the probe, the reverse standby made zero `Writer.Append` calls.

**Pin.** `TestWalReceiverAppendsFramesWhoseWalEndIsAhead` streams three
frames that all report the whole stream's end; the local WAL must equal the
primary's byte for byte. Against the old receiver it fails at once: a
56-byte frame grew the local WAL to 128.

**Not done (ledgered).** The overlapping prefix is trimmed, not overwritten:
PG's walreceiver writes the received bytes over its own (XLogWalRcvWrite).
goopg assumes they are identical, which holds for a standby whose WAL came
from this primary. A divergent local tail, the pg\_rewind case, is neither
detected nor repaired.
