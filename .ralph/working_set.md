(idle — nothing in flight)

## Loop summary (2026-07-12, loop #62)

**Nightly triage:** action-items batch `20260711-011536` (same as #58-#61) —
all 3 AI items already `[x]` in M-NIGHTLY (IsolationTimeouts +
TuplelockUpgradeNoDeadlock co-load flakes + PgWaldumpVacuumPruneRoundtrip).
No new nightly work.

**Task — M0110-0001 / n_distinct attribute option now consumed by planner.**
`ALTER TABLE ... ALTER COLUMN ... SET (n_distinct=<v>)` was stored on
catalog.Column.Options for pg_dump fidelity only; the planner ignored it.

Landed:
- internal/executor/operators_analyze.go: new `columnNDistinctOverride(col,
  rowCount)`; applied in analyzeRelationWith's per-column loop right after
  computeColumnStats (only for analyzed columns). Mirrors PG analyze.c:571-581
  (override baked in at ANALYZE time). positive v => absolute; v in [-1,0) =>
  |v|*RowCount (clamped -1, floored 1); 0/unset/n_distinct_inherited/other/
  malformed => no-op. Planner's existing columnNDistinctForChild
  (cardinality.go) reads it transparently — no plan-time change.
- internal/executor/operators_analyze_test.go: TestColumnNDistinctOverride
  (12-case table-driven parse contract), TestAnalyzeRespectsNDistinctOption
  (end-to-end through analyzeRelationWith).
- docs/design/0006-0006-n-distinct-attoption-override.md (new; sibling of
  0006-0005 stats-target-enforcement); README index row added.
- unimplemented_feat.json M0110-0001 open→resolved (surgical Edit, revalidated).
- deferral_ledger.md `-` row: negative fraction resolved at ANALYZE time vs
  PG's plan-time re-resolution against reltuples (needs signed NDistinct repr).

Gates: go build ./... clean; go vet ./internal/executor clean; ANALYZE test
group PASS; scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33 — no-op path, no TPC-H
col has the option). Planner/executor change → spotcheck ran per practice card.
pgbench smoke via pre-commit hook at commit.

Next loop: unimplemented_feat.json ~82 open. Bounded candidates: per-slot
catalog-xmin retention hook, KindDate BETWEEN codec carrier (m0003). Avoid:
WALInsertLock array / MultiXact WAL (large), TLS-blocked channel binding,
parallel executor (moot).

In-flight: none
