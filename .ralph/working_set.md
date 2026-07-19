Task: M0123-S2 sub-slice 2 part (a) — FuncExpr resolution VALIDATED + red-test
reconciled. COMPLETE this loop.

Findings: sub-slice-1 commit e85ccb53 actually shipped the FULL FuncExpr resolver
(gen-pg-proc-data emits pgProcRetTypeByOID; catalog.ProcResultType; resolveFuncCall
+ rebuildFuncExpr) but left resolver_expr_test.go asserting upper('x') UNSUPPORTED
→ `go test ./internal/pgnodes/` was RED at HEAD. Captured live PG18.3 adbin golden
for `b text DEFAULT upper('x')` (funcid 871 / rettype 25 / collid 100); resolver
output is BYTE-FOR-BYTE identical. Reconciled the test (accept upper('x') + golden
Out pin + round-trip case), fixed stale header comment + design doc 0123-0002.

Files: internal/pgnodes/resolver_expr_test.go (test reconcile), resolver_expr.go
(header comment), docs/design/0123-0002-pgnodes-scalar-resolver.md (FuncExpr moved
Deferred→Resolved), .ralph/fix_plan.md + deferral_ledger.md.

Gates run: go test ./internal/pgnodes/ GREEN; go build ./... clean; go vet clean;
live-PG18 golden capture (throwaway --no-sync cluster /tmp:5599); make
ralph-state-guard OK (auto-repaired); pgbench smoke via pre-commit hook on commit.

Next step (next loop): M0123-S2 sub-slice 2 remaining (b)(c)(d) — the risky/E2E
wiring half:
 (b) wire writeAttrdefRow (internal/executor/operators_ddl.go:13276) +
     sys_pg_statistic_ext.go stxexprs → NewBytesDatum(pgnodes.Out(ir)) when
     pgnodes.SupportsExpr else NewStringDatum(sqltext);
 (c) swap loadColumnDefaultsFromHeap / loadStatisticsExtFromHeap
     (internal/initdb/catalog_heap_reload.go) to pgnodes.Read→Rebuild on a
     '{' discriminator, standalone-unconditional per NamespaceDBOid;
 (d) adversarial PG18 standby-eval E2E: goopg CREATE TABLE t(a int DEFAULT 40+2,
     b text DEFAULT upper('x'), c int DEFAULT -1); PG standby INSERT DEFAULT
     VALUES asserted =(42,'X',-1) AND == goopg's own.
NOTE (b)/(c) are sibling paths — must land together (writer-only would break
goopg's own reload). Watch M0114 cache-hit reload path.

In-flight: none.
