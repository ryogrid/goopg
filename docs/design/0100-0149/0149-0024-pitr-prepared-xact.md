# 0149-0024 — Port recovery/t/023\_pitr\_prepared\_xact.pl

Status: done \(2026\-11\-25\). `TestPort_Recovery023PitrPreparedXactFull`
\(`internal/testport/recovery_023_pitr_prepared_xact_test.go`\) passes on all three pairings and is ungated:
all\-goopg, a PG 18.3 primary with a goopg PITR node, and a goopg primary with a PG 18.3 PITR node.

023 prepares a transaction, creates a restore point, and inserts one more row. It then runs archive recovery
\(init\_from\_backup `standby => 0`, has\_restoring\) to that restore point with recovery\_target\_action = 'promote'. On
the promoted node, COMMIT PREPARED must find the prepared transaction and make exactly its row visible. A checkpoint
on the new timeline must then survive an immediate stop.

A goopg PITR node lost the prepared transaction: "prepared transaction with identifier "fooinsert" does not
exist". Archive recovery's replay hooks covered commit and abort stamping only. The standby replayer's two\-phase
bookkeeping \(PrepareRedoAdd on XLOG\_XACT\_PREPARE, PrepareRedoRemove on COMMIT/ABORT PREPARED\) had no archive
counterpart.

- `xlog.ArchiveReplayHooks` gains `Prepare` and `PreparedFinish`, called for each replayed record past the redo point.
- `archiveReplayHooks` \(cmd/goopg\) registers prepared transactions in the transaction manager, as the standby does.
- At the end of archive recovery, `restoreReplayedPrepared` \(RecoverPreparedTransactions, now shared with standby
  promotion\) gives every still\-open prepared transaction its proc slot. It hands them to the server before it
  starts, skipping those already restored from pg\_twophase at startup.

Not done: a prepared transaction's pg\_twophase state file is not written at the end of archive recovery
\(PG's end\-of\-recovery checkpoint moves WAL\-only 2PC state to disk via CheckPointTwoPhase\). goopg keeps it in
memory and its WAL. The crash restart in 023 does not need it, since the transaction is committed by then.
