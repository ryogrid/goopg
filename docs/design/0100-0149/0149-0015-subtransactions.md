# 0149-0015 — Port recovery/t/012\_subtransactions.pl

Status: in progress. The port exists and is skipped unless `GOOPG_TAP_012=1`. It still needs 0015a \(slice 1 of 3 done\) and 0015b.

Upstream: `postgres/src/test/recovery/t/012_subtransactions.pl`; walsender.c `WalSndDone`, postmaster.c
`PM_SHUTDOWN` → `PM_SHUTDOWN_2`, xlog.c `StartupXLOG` (`recoveryTargetTLI`, `writeTimeLineHistory`), twophase.c.

## What the test checks

- Prepared transactions with savepoints survive a primary restart.
- A deep chain of plpgsql exception blocks \(127 and 201 subtransactions\) commits and replays.
- Prepared transactions with many subtransactions replay to a standby and stay invisible there. After promotion they
  can still be committed or rolled back.
- Twice, the primary is stopped, its standby promoted, and the old primary restarted as the new standby without
  pg\_rewind.

## Blockers \(Path\-to\-TAP\)

| link | state |
|---|---|
| `log_checkpoints` was not a registered GUC; goopg refused the test's postgresql.conf | fixed: registered \(bool, on, sighup\) |
| the old primary could not rejoin \(0015c\) | fixed, see below |
| goopg 2PC is in\-memory only: no PREPARE record, no pg\_twophase, lost at restart \(0015a\) | open |
| a goopg standby drops a replayed PREPARE; promotion cannot COMMIT/ROLLBACK PREPARED it \(0015b\) | open |

## 0015c — rejoining after promotion

- **Shutdown order.** PG's postmaster lets regular backends exit first. The checkpointer then writes the shutdown
  checkpoint, and only then do walsenders send the remaining WAL and wait for the standby to confirm its flush
  \(`WalSndDone`\) before exiting.
  - goopg used to close walsender connections with every other connection and write the checkpoint in
    `Runtime.Close`. The standby therefore ended one checkpoint record short. After promotion its switch point lay
    before the old primary's end of WAL, and the old primary refused to follow \("new timeline forked off … before
    current recovery point"\).
  - Now walsender connections leave the regular\-backend wait group once their startup packet says
    `replication`, and `Server.finishWalsenders` runs after the backends have drained:
    1. it calls `Config.ShutdownCheckpoint` \(`Runtime.ShutdownCheckpoint`, primary only; `Close` then skips its
       own\);
    2. it closes `replication.Config.Stopping`;
    3. each physical walsender sends to the flushed end, waits up to 10 s for the standby's flush, and ends with
       CopyDone;
    4. the server waits up to 30 s for the streams to end, then closes the connections.
  - An immediate stop skips the checkpoint and closes the walsenders at once.
- **History after following a timeline.** A standby that streamed onto its primary's newer timeline was promoted
  from the persisted `timeline_id`, which can still name the older timeline. The new history then skipped the
  followed one, and the next standby was told "requested timeline 2 is not in this server's history". Promotion now
  takes the timeline replay is on \(`recoveryTargetTLI`\).
- Test: `TestE2E_OldPrimaryRejoinsAfterPromotion`, two role swaps, all\-goopg and goopg primary over a PG 18.3
  standby.

## 0015a — durable two\-phase commit on a goopg primary

Slices:

1. **WAL records \(done 2026\-11\-24\).**
   - PREPARE TRANSACTION assigns an xid if the transaction has none. It then writes PG's `XLOG_XACT_PREPARE` and
     flushes it: the main data is twophase.c's state buffer, `xlog.EncodeTwoPhaseState`.
   - The buffer holds the 72\-byte `TwoPhaseFileHeader`, the gid, the committed subtransactions, the relations to
     drop on commit and on abort, and the `TWOPHASE_RM_END_ID` sentinel. Each segment is MAXALIGN'd, and total\_len
     counts the CRC.
   - The manager remembers the xid \(`MarkTwoPhase`\), so the transaction's end record is `XLOG_XACT_COMMIT_PREPARED`
     or `XLOG_XACT_ABORT_PREPARED`. That record has header xid 0 and the prepared xid in `xl_xact_twophase`.
   - The logical decoder and the slot restart anchor treat the `*_PREPARED` records like a commit or abort of that
     xid.
   - Byte oracle: a `pg_twophase` file PG 18.3 wrote is in `xlog/testdata`. The header, gid and subxact segments
     match; padding bytes are uninitialised in PG.
   - pg\_waldump decodes goopg's records as `PREPARE gid …; subxacts: …` and `COMMIT_PREPARED <xid> …`.
   - Movement: in 012's goopg\-primary / PG\-standby pairing, the PG standby now replays a goopg PREPARE and finishes it
     after promotion \(ROLLBACK PREPARED of the 201\-subxact transaction\). Before, the run stopped earlier.
2. **Restart restore \(next\).**
   - `CheckPointTwoPhase` writes `pg_twophase/<XID>` with a CRC for prepared transactions whose PREPARE is older than
     the redo point.
   - Startup reads those files \(`restoreTwoPhaseData`\) and the PREPARE records after redo. Their xids and subxids
     must stay in progress: neither swept aborted nor visible.
   - The gids are registered, so COMMIT/ROLLBACK PREPARED can finish them from any backend; this needs a finishing
     path that works without a live `Transaction`.
   - `pg_prepared_xacts` lists them.
3. **Standby \(0015b\).** PREPARE replay keeps the state \(`PrepareRedoAdd`\), and promotion goes through slice 2's
   restore.

## Not covered

- The walsender's drain and flush wait are fixed at 10 s instead of following `wal_sender_timeout`. Logical
  walsenders do not drain at shutdown.
- A goopg standby of a PG primary does not see user tables \(M0149\-0015d, filed\).
- The PREPARE record carries no resource\-manager records: no lock records for a standby to re\-acquire, and no
  pgstat or predicate\-lock records. It is not followed by a synchronous\-replication wait.
- `COMMIT PREPARED` / `ROLLBACK PREPARED` answer with the command tags `COMMIT` / `ROLLBACK`; PG sends
  `COMMIT PREPARED` / `ROLLBACK PREPARED`.
- No `XACT_XINFO_HAS_GID` under wal\_level=logical.
