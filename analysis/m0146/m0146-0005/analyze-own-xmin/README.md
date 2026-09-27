# M0146-0005 filed recon — ANALYZE sees its own transaction's rows

Recon item filed 2026-09-25 by M0146-0005c ("the executor test fixture's
ANALYZE records `RowCount: 0` for rows it cannot see"), resolved 2026-09-27.

## Verdict

**Real engine divergence**, not a fixture-transaction artefact.

`analyzeRelationWith` (`internal/executor/operators_analyze.go`) began a
nested transaction only to mint its snapshot, then passed **that** tx's XID
as `currentXID` to `transam.TupleVisible`. A tuple's own-xmin arm
(`h.Xmin == currentXID`, visibility.go) therefore never fired for rows the
session transaction inserted — they fell to `snap.SeesCommittedXID`, which
(rightly) reports an in-progress xid as not committed. `BEGIN; INSERT …;
ANALYZE;` recorded `RowCount 0` where PG 18.3 records the row count.

## Live A/B (ports :5542 goopg / :5541 scratch PG 18.3)

Pre-fix goopg:

    BEGIN; CREATE TABLE t(k int);
    INSERT INTO t SELECT g FROM generate_series(1,100) g;
    ANALYZE t;  → reltuples 0   (PG: 100)
    SELECT count(*) FROM t3;  → 25 rows visible to plain SELECT —
      general MVCC own-xid credit already worked; only ANALYZE's scan
      used the wrong "self" identity.

Post-fix (`goopg.txt` vs `pg18.txt`): byte-identical sequences —
`INSERT 100` → reltuples 100; `DELETE WHERE k>50` in the same txn →
reltuples 50; post-commit → 50. Own-inserted *and* own-deleted rows
follow PG's command-ordering arms (cmin/cmax vs curcid).

## Fix

`analyzeRelationWith` now credits `dsCtx.Tx.XID` as the own transaction,
gated on `mgr.IsXIDActive(dsCtx.Tx.XID)` — PG's
`TransactionIdIsCurrentTransactionId`: only a still-open session
transaction counts its own writes. A committed or aborted caller XID
(fixture commit-then-analyze patterns like `seedRowsAndAnalyze`) falls
through to the snapshot check like any other xid. `dsCtx == nil`
(autovacuum's `AnalyzeRelationSampled`, the test-only `analyzeRelation`
wrapper) keeps the nested tx's XID — correct, since PG's autovacuum
worker analyzes in its own transaction and sees only committed rows.

## Fixture aftermath (why the report looked fixture-only)

- `runDDL` already calls `advanceStmtCounter` per statement, so a
  fixture `INSERT …` then `ANALYZE` through the DDL harness now counts
  the rows — same as the wire path.
- Rows written *and* analyzed at the **same** command id stay invisible
  (`cmin >= curcid`), which is PG's own-command rule, not a bug: a
  statement never re-scans its own writes. Fixture tests that need the
  rows counted must advance the statement counter first; `setFixtureStats`
  (M0146-0005c) remains the pin where a test wants stats without ANALYZE.

## Regression test

`TestAnalyzeSeesOwnUncommittedInserts` in
`internal/executor/operators_analyze_test.go`: INSERT (no commit) →
`advanceStmtCounter` → `analyzeRelationCtx` → `RowCount == 25`.

## Gates

- `go test ./internal/executor/` — PASS (incl. all prior ANALYZE suites;
  the fixture commit-then-analyze tests still pass via the IsXIDActive
  gate).
- Units (RALPH_PRECOMMIT_SCOPE=units) — PASS.
- tpch-spotcheck — PASS (Q12=2, Q13=33).
- TPC-H acceptance arm `m0146-analyze-ownxid` vs
  `tmp/m0145-0008m/arm-on.txt` — see gates.txt once stamped.
- TPC-DS SF0.25 sweep + fireset — see gates.txt once stamped.
