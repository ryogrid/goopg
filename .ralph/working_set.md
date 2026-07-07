Task: M0122-0006 — "index column order (ASC/DESC/NULLS) across restart"
sub-item. COMPLETE and committed this loop.

Files: internal/catalog/catalog.go (pg_index Virtual builder's indoption
cell, was buildZeroVec, now real INDOPTION_DESC/NULLS_FIRST bitmask),
internal/catalog/codec.go (PGIndexRow.IndOption + DecodePGIndexPhysicalRow
walks past indcollation/indclass varlena to decode it; new
pgIndexAlign4/pgVarlenaTotalLen helpers), internal/executor/
pg18_user_catalog_rows.go (buildUserPGIndexRow computes real indoption),
internal/executor/operators_ddl.go (THE real fix: createBTreeIndex gained
colDescending/colNullsFirst params, set on idx BEFORE its WAL emission —
previously execCreateIndex set them in a post-call block AFTER
createBTreeIndex had already emitted the WAL record, so ColDescending was
correct in-memory/heap but permanently lost on an uncheckpointed crash
restart; all 16 createBTreeIndex call sites updated), internal/wal/
recovery.go (CreateIndexPayload.ColDescending/ColNullsFirst, encoded as
TWO TRAILING append-only numCols-byte blocks — NOT interleaved — for
backward compat with existing on-disk WAL), internal/initdb/open.go +
index_ddl_recovery.go (RegisterIndexDuringRecovery threads the two new
[]bool params through both recovery drivers), plus test files (executor
round-trip test, wal encode/decode + backward-compat tests, initdb
true-crash restart test) and docs (new design doc
docs/design/0122-0006-index-column-order-restart-persistence.md,
docs/design/README.md row, .ralph/fix_plan.md M0122-0006 bullet,
.ralph/deferral_ledger.md new row for the sibling gap below).

Key symbols: createBTreeIndex (operators_ddl.go) — colDescending/
colNullsFirst now set immediately after `idx, err :=
o.ctx.Catalog.CreateIndex(...)`, before the WAL block. execCreateIndex —
now computes colDescending/colNullsFirst from s.ColOrders BEFORE calling
createBTreeIndex (moved up from the old post-call resync block, which no
longer re-sets these two fields). wal.EncodeCreateIndex/DecodeCreateIndex
— trailing-block format, `switch remaining { case 0: old-format; case
2*numCols: new-format; default: error }`.

Findings: THREE independent bugs stacked on this milestone item — (1) live
pg_index.indoption always zero (Virtual builder), (2) heap-shadow-row
indoption always zero + decoder never reached it, (3) the real
crash-durability bug: WAL payload built before idx.ColDescending was set
by the caller. (3) only reproduces via a TRUE uncheckpointed SIGKILL — a
plain `rt.Close()` in a test (or `CHECKPOINT` + kill) performs a shutdown
checkpoint that flushes the heap row and masks it (heap-recovery path wins
the RegisterIndexDuringRecovery dedup race). Discovered via live manual
testing against the real cmd/goopg binary (unit tests alone did NOT catch
this — TestCreateIndexColumnOrderingSurvivesRestartViaWAL had to be
rewritten to use the TestCrashRecoveryReplaysWALAfterUncleanShutdown
true-crash pattern: flush WAL, close WAL+StorageMgr directly, skip
Pool.Close). Also found (and fixed as a required prerequisite): my first
WAL-encode attempt interleaved the desc/nullsFirst flag bytes with each
column's bytes, which broke decoding pre-existing on-disk WAL in
bench/tpch/runtime_goopg/data — scripts/tpch-spotcheck.sh caught it
immediately (`wal: create-index payload truncated at column 0 body`).
Fixed by switching to two trailing append-only blocks instead.

Sibling gap (NOT fixed, ledger row appended): wal.CreateIndexPayload never
carried predicate/INCLUDE columns/opclass/collation/fillfactor/dedup either
— same "create now, patch later, WAL emitted too early" pattern, same
uncheckpointed-crash-only exposure. Reasoned from code, not reproduced
end-to-end. Resume point in the ledger row.

Next step: pick the next task. M-NIGHTLY is clean (all 8 items from run
20260707-000712 checked/resolved, no new action-items.md run since).
Candidates: (a) the sibling deferred gap above (wal.CreateIndexPayload
missing predicate/opclass/collation/fillfactor/dedup) — well-scoped
follow-up using the exact pattern+test technique just established; (b)
M0122-0006's remaining items (pg_tablespace visibility, fuller pg_index
heap persistence); (c) M0122-0007/0008 quick wins (DDL/admin commands,
auth/roles — SASLprep/channel binding/scram_iterations still open in
0122-0008); (d) resume M0119-0004/0005/0006/0007 per the Current Priority
banner (M0119-0004's per-database catalog-isolation gap, M0119-0006's
opclass/pg_amproc-UPDATE-path gap documented in the 2026-07-07 M0122-0001
ledger row).

Gates run: go build ./... clean, go vet ./... clean. go test
./internal/wal/... ./internal/initdb/... ./internal/catalog/...
./internal/executor/... ./internal/planner/... ./internal/analyzer/...
./internal/parser/... PASS. go test -short ./... (excluding tpch/testport)
PASS, full suite. scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33) — this ALSO
regression-tests the WAL backward-compat fix (failed before it).
RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh PASS (0 failed,
all 3 workloads). Live e2e: real cmd/goopg binary, actual kill -9 crash,
verified pg_get_indexdef + SELECT indoption FROM pg_index both correct
post-restart. make ralph-state-guard: 2 benign issues auto-repaired
(identical pattern to every prior loop — status/progress running-vs-
completed reconciliation).

In-flight: none. All manual verification servers/processes killed and
cleaned up (/tmp/goopg-verify*).
