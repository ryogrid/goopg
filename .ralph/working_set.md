(idle — M0102-0010 recovery/FPI checksum-replay validation gate COMPLETE & committed this loop)

Loop #32 landed gate (a) of two for the deferred `--data-checksums` default-ON
flip: a crash-recovery test proving a checksum-enabled cluster replays its WAL
(full-page images included) with valid pd_checksums after an unclean shutdown.
- File: internal/initdb/recovery_test.go
  `TestCrashRecoveryReplaysChecksummedClusterCleanly` (modeled on the existing
  TestCrashRecoveryReplaysWALAfterUncleanShutdown + data_checksums_test.go's
  on-disk VerifyPage walk). Reuses collectRelationFiles.
- Key path proven: wal/recovery.go restoreDecodedXLogBlockImage →
  writeBlockOrExtend → Manager.WriteBlock → checksummedForWrite recomputes
  pd_checksum per replayed block (architecturally already correct; now tested).
- Docs: docs/design/0102-0019-initdb-data-checksums.md "Remaining: default-ON
  flip" now records gate (a) DONE, gate (b) pending. fix_plan.md M0102-0010
  progress note added.
- Gates: gofmt/vet clean; go test ./internal/initdb PASS; go test -race
  ./internal/storage ./internal/wal PASS.

Default stays OFF. The flip is still blocked on **gate (b): standby-read /
physical-replication validation** — a checksummed goopg primary must stream to a
PG (or goopg) standby whose read path verifies pd_checksum. That is the single
concrete next step toward the flip (then the flip is a one-line default change).

⚠️ WORKING-TREE CONTAMINATION (still present — separate session, DO NOT commit):
~18 modified files + 2 new test files belong to an UNRELATED partition
generated-column-override feature: analyzer/analyzer.go, catalog/catalog.go
(+_test), executor/* (operators.go, operators_ddl.go, operators_join_agg.go,
operators_lockrows.go, opnode.go), mvcc/subxact_visibility.go, parser/ast.go,
parser/ddl.go, planner/* (bushy, nl_index_join, plan, planner, unnest),
server/dispatch.go + parser/gen_override_test.go +
executor/partition_gen_override_test.go. Tree builds clean WITH them. I staged
ONLY my own files (recovery_test.go, design doc, fix_plan, working_set,
progress.json). The override-feature owner should commit or revert the rest.
