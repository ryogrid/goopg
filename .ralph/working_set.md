Task: M0143-0008b — DROP CONSTRAINT rollback-undo for CHECK/FOREIGN
KEY/NOT NULL. LANDED AND COMMITTED this loop (commit fdcee45d8).

Files: internal/executor/session.go (new DropConstraintUndoEntry type +
RecordDropConstraintUndo/TakePendingDropConstraintUndos, mirrors
NotNullUndoEntry), internal/executor/operators_ddl.go
(snapshotDropConstraintState helper; recording calls added at all three
execAlterTableDropConstraint branches — CHECK, FOREIGN KEY, NOT NULL),
internal/executor/operators_tx.go (ProcessRollbackUndos restores the
snapshot's 4 slices + per-column NotNull map onto the live *catalog.Table
pointer), internal/testport/p0e5b_alter_drop_constraint_rollback_undo_test.go
(new, 3 tests), docs/design/0100-0149/p0-e4-catalog-xmax-loss-repro.md
(new "M0143-0008b" section appended), docs/design/README.md (p0-e4 row
updated), .ralph/fix_plan.md (M0143-0008b [x] + Done note),
.ralph/deferral_ledger.md (1 new row: M0143-0002 live-confirmed as the
blocker for testing the FK undo against a non-default DB).

Key symbols: DropConstraintUndoEntry (session.go) — wholesale snapshot of
CheckConstraints/NamedChecks/ForeignKeys/NotNullConstraints/per-column
NotNull; snapshotDropConstraintState (operators_ddl.go) builds it;
collectNotNullCascadeClosure (pre-existing, reused unchanged — it's a
generic inheritance/partition tree walk despite the name, not actually
NOT-NULL-specific) supplies the cascade-reachable child set for the CHECK
and NOT NULL branches (FK has no cascade, FKs aren't inherited).

Findings: while writing the FK sub-test, live-reproduced the ALREADY-FILED
M0143-0002 bug (catalog.go:22238 DropForeignKeyConstraint hardcodes
DefaultDBOid) — on a non-default DB (db "r", the pattern the sibling P0-E5
tests use) a plain COMMITted DROP CONSTRAINT on an FK silently no-ops (FK
stays enforced), independent of ROLLBACK entirely. Confirmed via 2 throwaway
probe tests (written, run, then deleted — not committed) before rewriting
the real test to run against the cluster's default-db handle instead, where
the mutation genuinely happens. All 3 tests independently verified to fail
when the fix is reverted (git stash of the 3 touched files) and pass when
restored — not just green-by-construction.

Next step: banner item 0 still gates on P0-E7 (only unlocked once P0-E6 —
owner-run, [!] — completes); "while P0-E6 waits" sub-order: M0143 tasks
whose gates don't need TPC-H data, then recon-only tasks (items 3-6), then
M-NIGHTLY. M0143's remaining `[ ]` items not gated on TPC-H data:
M0143-0001 (in-process test crossing a DATABASE boundary — filed as
"highest leverage of the six"), M0143-0002 (the FK DefaultDBOid bug just
live-confirmed above — a natural next pick, same subsystem/muscle memory,
concrete repro already in hand from this loop), M0143-0003 (pg_constraint
0-rows-after-restart), M0143-0004 (PhysicalTypeIsVarlena IsArray),
M0143-0005 (ParamRef LIMIT+DISTINCT), M0143-0006 (parser's 60 failing
tests). Re-check `## Current Priority` fresh next loop per the Precedence
rule before picking — do not just take this suggestion if the banner moved.

Gates run: `go build ./...` clean. `go test ./internal/executor/...
./internal/catalog/...` PASS. `go test -v -run
'TestPort_P0E4|TestPort_P0E5|TestPort_M0143_0008b' ./internal/testport/`
PASS 8/8 (verified against BOTH the fixed tree and the reverted tree via
git stash, to rule out false-positive tests). `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` — all packages PASS except the
pre-existing, already-documented `internal/parser` GroupedJoinUnaliased
AST-drift (unrelated, no parser file touched this loop).
`scripts/tpcds-sf025-regression.sh sweep` (re-run against staged content) —
PASS=96 MISMATCH=0 ERROR=0 TIMEOUT=0, plan-shapes 99/99 identical,
gate-stamp PASS. `scripts/tpch-spotcheck.sh` and
`scripts/tpch-acceptance-arm.sh off <tmpfile>` — both SKIP-BLOCKED (exit
3/0-with-SKIP-BLOCKED-stamp) by the `:65433` evidence hold (accepted G6
exception, ledgered in the commit body same as P0-E5). Pre-commit hook's
pgbench smoke: PASS. `make ralph-state-guard`: found status/progress
inconsistent from the previous loop's clean-exit marker, auto-repaired
(`progress reconciled to in_progress`), then clean.

In-flight: none. No servers or background processes left running (all
test clusters use cluster.New's t.Cleanup). The two throwaway FK-diagnosis
probe test files (internal/testport/zz_fk_probe_test.go and
/tmp/fk_probe_test.go) were deleted before commit, not left behind.
