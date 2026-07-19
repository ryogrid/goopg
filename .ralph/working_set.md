Task: M0123-S2 sub-slice 1 — pgnodes scalar resolver/rebuild/shape-check. COMPLETE + committed this loop.

Files (all new, internal/pgnodes/):
- resolver_expr.go — ResolveExpr(parser.Expr,targetType)→Node (int4/int8 lit, unary-minus fold, text lit, binary OpExpr via S0 LookupOperatorForNode); ErrUnsupported = fall back to SQL text.
- rebuild.go — Rebuild(Node)→parser.Expr (reload inverse); opno→spelling via lazy reverse index over catalog.PGOperatorAllEntries.
- unsupported.go — SupportsExpr all-or-nothing predicate.
- resolver_expr_test.go — 10 subtests (canonical-Out pins, 40+2→OpExpr, resolve→Out→Read→Rebuild→re-resolve round-trip, accept/reject table).

Gates run (all green): go build ./... ; go vet ./internal/pgnodes ; go test ./internal/pgnodes (10 new + 4 S1 pass); make ralph-state-guard consistent; pgbench smoke via pre-commit hook.

Next step (M0123-S2 sub-slice 2 — its own gated commit, the risky/E2E half):
  (a) FuncExpr: extend cmd/gen-pg-proc-data -names to emit pgProcRetTypeByOID leaf
      map (generator already parses prorettype @main.go:247) + catalog accessor,
      then handle *parser.FuncCall in resolve/rebuild.
  (b) wire writeAttrdefRow (operators_ddl.go:13272) + sys_pg_statistic_ext.go
      stxexprs → NewBytesDatum(pgnodes.Out(ir)) when SupportsExpr else NewStringDatum.
  (c) swap loadColumnDefaultsFromHeap/loadStatisticsExtFromHeap
      (initdb/catalog_heap_reload.go) to pgnodes.Read→Rebuild on '{' discriminator.
  (d) adversarial standby-eval E2E: DEFAULT 40+2/upper('x')/-1; PG18 standby
      INSERT DEFAULT VALUES asserted =(42,'X',-1) AND ==goopg's own.
  See deferral_ledger 2026-07-19 M0123-S2 sub-slice 1 row for full resume points.

In-flight: none.
