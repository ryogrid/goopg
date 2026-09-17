Task: P0-E5 — fix the catalog-loss defect + M0143-0008 together.
LANDED AND COMMITTED this loop (commit 4f6f81734).

Files: internal/initdb/catalog_heap_reload.go (catalogRowLive B0.2 rule +
scanCatalogHeapRows pre-filter fix), internal/initdb/catalog_heap_reload_test.go
(TestCatalogRowLive B0.2 case flipped), internal/initdb/open.go (3 more
duplicate inline xmax pre-filters fixed: loadUserIndexesFromHeapForDB x2,
pg_statistic reload — all now delegate to catalogRowLive),
internal/executor/session.go (new AlterIndexUndoEntry/NotNullUndoEntry +
Record/Take methods), internal/executor/operators_ddl.go
(adoptExistingIndexAsConstraint/finishPrimaryKeyConstraint record undo;
execAlterTableDropConstraint's UNIQUE/EXCLUDE/PK branches record
DDLDropUndoEntry), internal/executor/operators_tx.go (ProcessRollbackUndos
consumes both new undo types + nil-Table guard for DDLDropUndoEntry),
internal/testport/p0e4_catalog_xmax_loss_test.go (3 t.Skip removed, all
PASS; to_regclass probe swapped for `::regclass` cast — to_regclass itself
is a pre-existing unrelated gap, ledgered 0168f), internal/testport/
p0e5_alter_rollback_undo_test.go (new, 2 live-session M0143-0008 tests),
docs/design/0100-0149/p0-e4-catalog-xmax-loss-repro.md (P0-E5 section
appended), docs/design/README.md (p0-e4 row updated), .ralph/fix_plan.md
(P0-E5 [x], M0143-0008 [x], new M0143-0008b [ ] filed), .ralph/deferral_ledger.md
(2 new rows: subxact-CLOG residual gap; CHECK/FK/NOT-NULL DROP CONSTRAINT
undo gap).

Key symbols: catalogRowLive (catalog_heap_reload.go:43) — rule 2 now
"non-zero xmax dead unless clog.GetStatus(xmax)==Aborted"; scanCatalogHeapRows
(:~105) — removed its OWN duplicate xmax pre-filter that shadowed
catalogRowLive for every production call (THIS was the real bug once
catalogRowLive alone was fixed and the P0-E4 tests still failed — caught by
actually running the tests live, not just building). AlterIndexUndoEntry/
NotNullUndoEntry (session.go) + collectNotNullCascadeClosure/
snapshotNotNullState (operators_ddl.go, new helpers).

Findings: two distinct bugs found beyond the design doc's original
diagnosis, both fixed in this commit: (1) scanCatalogHeapRows's inline
pre-filter duplicated catalogRowLive's OLD logic and ran first, so the
catalogRowLive fix alone was a no-op against production — only surfaced by
running the P0-E4 regression tests live and seeing them still fail after
the "fix"; (2) to_regclass() is entirely unimplemented in goopg (42883,
pre-existing, ledgered 0168f) — the ORIGINAL P0-E4 test used it as a probe,
which made an unimplemented-function ERROR indistinguishable from "table
lost" (both collapsed to empty string via the query's error-swallowing
`if err == nil` guard), so the test looked like it was still reproducing
the bug when the real defect was already fixed. Confirmed via a
general-purpose subagent's independent code-reading pass (grepped for
"to_regclass" dispatch, found no case arm, confirmed the 42883 error) before
trusting it. Fixed by switching the test's probe to `'t'::regclass::text`
(implemented, confirmed working — regIdentifierInput's regclassin port).

Next step: banner item 0 says P0-E6 is owner-run/not selectable next
([!], the loop never works around it). Per banner's "while P0-E6 waits"
sub-order: select M0143 tasks whose gates don't need TPC-H data first —
**M0143-0008b** (CHECK/FK/NOT-NULL DROP CONSTRAINT rollback-undo, filed this
loop, Parent: M0143-0008) is a strong candidate: pure in-memory catalog
undo, testable via unit/testport tests with no TPC-H data dependency,
directly continues this loop's pattern (DROP-direction sibling of
NotNullUndoEntry, reusing collectNotNullCascadeClosure's pre-walk shape).
If the banner has moved since this was written, re-check `## Current
Priority` fresh per the Precedence rule.

Gates run: `go build ./...` clean throughout. `go test ./internal/initdb/...
./internal/catalog/... ./internal/executor/...` PASS. `go test -v -run
'TestPort_P0E4|TestPort_P0E5' ./internal/testport/` PASS (5/5).
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — all packages
PASS except the pre-existing, already-documented `internal/parser`
GroupedJoinUnaliased AST-drift (unrelated, no parser file touched this
loop). `scripts/tpcds-sf025-regression.sh sweep` (re-run against the staged
tree) — PASS=96 MISMATCH=0 ERROR=0 TIMEOUT=0, plan-shapes 99/99 identical,
gate-stamp PASS. `scripts/tpch-spotcheck.sh` and
`scripts/tpch-acceptance-arm.sh` — both SKIP-BLOCKED (exit 3) by the
`:65433` evidence hold, stamps recorded (the accepted G6 exception per
fix_plan's P0-E5 text + AGENT.md G1); ledger names P0-E7 as re-run owner.
Pre-commit hook's pgbench smoke: PASS. `python3
scripts/ralph-lineage-guard.py`: clean (had to move M0143-0008b's `Parent:`
field onto its own body-line start — the guard's regex requires it either
inline on the task's own header line or at the very start of a continuation
line, not mid-line). `make ralph-state-guard`: clean both before and after
commit.

In-flight: none. No servers or background processes left running (all test
clusters use cluster.New's t.Cleanup). Temp files from manual gate probing
(/tmp/p0e5-arm-off.txt) are outside the repo, not cleaned up but harmless.
