Loop COMPLETE + committed: M0123-S1 (canonical pg_node_tree scalar serializer).
All 4 M-NIGHTLY items from run 20260717-010601 were already closed (race fixed
faf9c7da; 3 regress items STALE/pass-at-HEAD), so this loop advanced M0123 (#3
priority, the wal-pg-nodetree branch focus).

**Task (done):** M0123-S1 — new leaf pkg `internal/pgnodes`: a byte-faithful
PG18 `pg_node_tree` serializer (`Out`) + reader (`Read`) for the scalar IR
(Const/FuncExpr/OpExpr/RelabelType/CoerceViaIO/SQLValueFunction).

**Files:** internal/pgnodes/{ir,datum,outfuncs,readfuncs,pgnodes_test}.go (new);
docs/design/0123-0001-pgnodes-scalar-serializer.md (new) + README.md index;
.ralph/fix_plan.md (S1 → [x]); .ralph/deferral_ledger.md (S2/S3/S4 row).

**Key symbols:** pgnodes.Out/Read, outDatum (by-value 8-byte word signed
decimals; by-ref varlena header VARSIZE<<2), byvalWord/textVarlena, tokenizer
(pg_strtok port), readDatum. Field order mirrors postgres outfuncs.c per tag.

**Gates run (all green):** go build ./... OK; go vet ./internal/pgnodes OK;
go test -v ./internal/pgnodes 20/20 subtests PASS — Out is byte-identical to
REAL PG18.3 pg_attrdef.adbin goldens (captured from a live throwaway server,
now torn down). make ralph-state-guard consistent (auto-repaired the recurring
stale completed marker). pgbench smoke = the pre-commit hook at commit time.

**Next step:** M0123-S2 — resolver_expr.go (goopg parser.Expr + catalog +
S0 LookupOperator/ProcForNode → IR) + rebuild.go + unsupported.go; wire
writeAttrdefRow / stxexprs writer to emit pgnodes.Out(ir) when supported;
swap loadColumnDefaultsFromHeap / loadStatisticsExtFromHeap to pgnodes.Read →
rebuild (1-byte discriminator: canonical dumps begin `{`). Adversarial
standby-eval E2E gate (see fix_plan M0123-S2 + ledger 2026-07-19 row).

**In-flight:** none.
