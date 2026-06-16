Task: DU-002 slice 115 — sequence-dump downstream links (COMPLETE, committing).
Pivoted off exhausted domain-IN-values track to the sequence object surface.

Files:
- internal/catalog/catalog.go — added `SeqParams` struct + `SequenceParamsFunc`
  hook (near VirtualSpecLockRowsFunc); replaced empty pg_sequence (OID 2224)
  VirtualRows stub with a builder iterating `c.tables` for IsSequence (seqrelid=
  Table.OID; params via the hook).
- internal/executor/operators_sequence.go — `init()` sets
  catalog.SequenceParamsFunc = sequenceParamsForCatalog (LookupSequence-backed);
  + seqTypeOID helper (smallint=21/integer=23/bigint=20).
- internal/executor/operators_pg_get_sequence_data.go — real SRF (was 0-row
  stub): outerSlot+BindLateralOuter, evalExprSlot arg, verifyHeapamResolveTable,
  project sequence VirtualRows [last_value,log_cnt,is_called] → (last_value,is_called).
- internal/executor/operators_pg_get_sequence_data_test.go — TestPgGetSequenceDataPopulated.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 115 section.
- .ralph/fix_plan.md — loop #79 progress note.

Key symbols: catalog.SeqParams, catalog.SequenceParamsFunc, pgSequence.VirtualRows,
sequenceParamsForCatalog, seqTypeOID, pgGetSequenceDataOp, verifyHeapamResolveTable,
evalExprSlot, LookupSequence, AllSequenceInfos.

Findings: sequences ARE catalog Tables (IsSequence, OID=seqrelid) created by
execCreateSequence; metadata lives ONLY in executor seqRegistry (single source of
truth) → hook pattern, no catalog→executor import. Links are inert until pg_class
lists the sequence (relkind='S'), so this slice is regression-free; e2e fixture has
no sequence so pg_sequence stays empty there (dump unchanged, verified).

Gates run: build+gofmt OK; catalog/planner/executor(full)/initdb unit PASS;
TestPort_PgDumpConnectionSetup PASS (2.19s, unchanged); pgbench pre-commit smoke on commit.

Next step: SLICE 116 — flip pg_class VirtualRows to emit relkind='S' for IsSequence
tables (stop skipping at the `t.Virtual && t.View==nil && !t.IsMatView` continue);
add a CREATE SEQUENCE to the TestPort_PgDumpConnectionSetup fixture; assert the dump
emits a byte-identical `CREATE SEQUENCE` + `SELECT setval(...)` vs real pg_dump 18.3.
Watch: correlated-lateral SRF path under non-empty pg_sequence; pg_dump getOwnedSeqs/
pg_depend must NOT emit a spurious `OWNED BY` for a standalone sequence; sequence's
relnatts=3 (last_value/log_cnt/is_called) — confirm pg_dump skips column dump for 'S'.
