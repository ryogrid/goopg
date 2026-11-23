# 0149-0015 — Port recovery/t/012\_subtransactions.pl

Status: done. `TestPort_Recovery012Subtransactions` passes all\-goopg and with PG 18.3 as primary or standby.

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
| PG refused goopg\-written pg\_proc rows: text\[\] ndim 0, pseudo\-type prorettype 25 \(0015e\) | fixed |
| goopg 2PC is in\-memory only: no PREPARE record, no pg\_twophase, lost at restart \(0015a\) | fixed |
| a goopg standby drops a replayed PREPARE; promotion cannot COMMIT/ROLLBACK PREPARED it \(0015b\) | fixed |

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
2. **Restart restore \(done 2026\-11\-24\).**
   - `transam` keeps a prepared\-transaction registry \(`twophase_registry.go`, twophase.c's `TwoPhaseState`\): xid,
     subxacts, gid, state bytes and the PREPARE end LSN.
   - `CheckPointTwoPhase` runs inside each checkpoint once the data files are synced. It writes `pg_twophase/<XID>`
     \(state \+ CRC\-32C, fsynced\) for every transaction whose PREPARE ended at or before the redo point. The CRC was
     checked against a PG 18.3 file.
   - At a primary's startup, between the clog replay and the abort sweep, `initdb.restoreTwoPhaseData` reads the
     files. It then walks the WAL from redo: PREPARE adds an entry, COMMIT/ABORT PREPARED removes it and its file.
   - Each survivor gets a dedicated proc slot holding its xid, with its subxacts linked and nextXid moved past them
     \(`Manager.RestorePrepared`\). The sweep skips those xids \(`MarkUnknownAsAbortedExcept`\).
   - The server registers the survivors by gid. COMMIT/ROLLBACK PREPARED finishes one through `Manager.Commit` /
     `Rollback` of its slot, which writes the `*_PREPARED` record. The state file is removed once that record is
     written.
   - Tests: `TestE2E_PreparedXactSurvivesRestart` \(fast stop via the file, immediate stop via WAL\), plus registry
     unit tests.
   - Movement: 012 check 1 passes in all three pairings \(0 → 6 rows\).
3. **Standby \(0015b, done 2026\-11\-24\).**
   - The stream replayer hands each `XLOG_XACT_PREPARE` to a two\-phase hook \(`SetTwoPhaseReplayHooks`\), and the
     standby registers it \(`PrepareRedoAdd`\). Each COMMIT/ABORT PREPARED removes the entry and its file
     \(`PrepareRedoRemove`\). The xid stays unknown in clog, so hot\-standby snapshots keep it invisible.
   - Promotion runs `restorePreparedAtPromotion` inside `finalizePromotion`, before pg\_control is stamped
     `DB_IN_PRODUCTION` \(when `pg_ctl promote` returns\) and before recovery snapshots end. Each survivor gets a
     dedicated slot and is handed to the running server \(`Server.AddRecoveredPrepared`\).
   - The order matters twice. Restoring after the stamp let a COMMIT PREPARED race ahead and find no gid. Restoring
     after recovery snapshots end made the prepared rows read as committed: an unknown xid below xmin counts as
     committed.
   - Tests: `TestE2E_PreparedXactSurvivesPromotion` \(goopg primary → goopg standby, commit and rollback\). 012's
     all\-goopg and pg18\-primary pairings pass end to end.

## 0015e — the last pairing \(goopg primary, PG 18.3 standby\)

- The baton's "replay stall" was wrong. pg\_waldump of the rejoined node's WAL showed PG never ran the segment's
  statements. The test's `psql` ignores errors \(Cluster.pm's `psql` does too\), so logging stderr showed PG refusing
  the function goopg had created:
  - "cannot change return type of existing function";
  - "proargnames must have the same number of elements as the function has arguments".
- Cause 1: `pgTextArrayBytes` never set the ArrayType `ndim`. Every goopg\-written `text[]` catalog value — proargnames,
  reloptions, proconfig, pg\_statistic text stavalues — was an empty array to PG. It now writes ndim 1, and PG's 16\-byte
  empty array for no elements. goopg's own decoders read the dims word and are unaffected.
- Cause 2: `TypeNameToOID` knows no pseudo\-types and returns text. `RETURNS void` was written as prorettype 25.
  `PGProcTypeOID` now maps PG's pseudo\-types \(void, trigger, record, any\*, handlers, …\) to their pg\_type OIDs. The
  change is scoped to the pg\_proc writer.
- With both fixed, 012 passes in all three pairings, and the env gate is gone \(R\-012 `port`\).

## Not covered

- The walsender's drain and flush wait are fixed at 10 s instead of following `wal_sender_timeout`. Logical
  walsenders do not drain at shutdown.
- A goopg standby of a PG primary does not see user tables \(M0149\-0015d, filed\): neither one already in the base
  backup, nor one created while the standby streams. One created after the backup but before the standby starts is
  seen.
- 0015f \(filed\): `CREATE OR REPLACE FUNCTION` on a goopg node promoted from a PG primary's backup panics in
  `cmpKeyProcNameArgsNsp`. PG's suffix\-truncated pivot tuples in `pg_proc_proname_args_nsp_index` are shorter than
  the key goopg compares. 012 passes regardless: the function already exists, and Cluster.pm's `psql` ignores the
  error.
- A standby does not write pg\_twophase files at restartpoints, and its startup does not restore prepared
  transactions from files or WAL: restoreTwoPhaseData runs only on a primary.
- The PREPARE record carries no resource\-manager records: no lock records for a standby to re\-acquire, and no
  pgstat or predicate\-lock records. It is not followed by a synchronous\-replication wait.
- `COMMIT PREPARED` / `ROLLBACK PREPARED` answer with the command tags `COMMIT` / `ROLLBACK`; PG sends
  `COMMIT PREPARED` / `ROLLBACK PREPARED`.
- No `XACT_XINFO_HAS_GID` under wal\_level=logical.
- `pg_prepared_xacts` / `pg_prepared_xact\(\)` list nothing: the view is in the catalog, but no function feeds it.
- A restored prepared transaction holds none of its locks again \(`lock_twophase_recover`\). Its pgstat counters are
  lost. The state file is removed after the end record is appended, without waiting for the abort's flush.
