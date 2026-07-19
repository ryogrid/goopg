Task: M0123-S3 sub-slice 2 part (a) — pgnodes view-query resolver.
COMPLETE + committed this loop (pure internal/pgnodes, no engine wiring).

Landed: `ResolveViewQuery(*parser.SelectStmt, RelationResolver) (*Query, error)`
(resolver_query.go) — goopg single-base-relation SELECT view → IR Query whose
OutRuleAction bytes == PG18 pg_rewrite.ev_action for the same DDL. Computes Var
varno/varattno/syn, selectedCols +7 bias (-FirstLowInvalidHeapAttributeNumber),
resorigtbl/resorigcol, resname, fixed RTE_RELATION/AccessShareLock/ACL_SELECT/
perminfoindex=1 skeleton. RelationResolver leaf interface keeps executor import
out. Extracted buildOpExpr/buildFuncExpr/funcCallGuard from resolver_expr.go so
scalar+query resolvers build byte-identical nodes (S2 goldens still green).
Gotcha found: plain single-table SELECT fills BOTH sel.From (len 1) AND
sel.FromExprs (one FromExpr, empty Joins) → reject joins on len(Joins)!=0.

Files: internal/pgnodes/resolver_query.go (new), resolver_query_test.go (new),
resolver_expr.go (extracted shared builders), docs/design/0123-0004-*.md
(retitled sub-slices 1–2a + resolver section) + README index, .ralph/fix_plan.md
(S3 sub-slice 2a note) + deferral_ledger.md (new row).

Key symbols: pgnodes.ResolveViewQuery, RelationResolver, RelationInfo,
ColumnInfo, queryScope (resolveExpr/resolveColumnRef/resolveTarget/selectedCols),
buildOpExpr, buildFuncExpr, funcCallGuard, selectedColsBias.

Gates run: go build ./... clean; go vet ./internal/pgnodes/ clean; gofmt -l =
only pre-existing resolver_expr_test.go (version mismatch, not mine); go test
./internal/pgnodes/ GREEN incl. TestResolveViewQuery (2 live PG18.3 goldens,
byte-for-byte) + RoundTrip + Structure + Unsupported(10). pgbench smoke via
pre-commit hook.

Next step (next loop): M0123-S3 sub-slice 2 part (b) — rebuild.go: add Query/
RangeTblEntry/RTEPermissionInfo/FromExpr/RangeTblRef/TargetEntry/Var arms
rebuilding a goopg view AST for the reload path (inverse of ResolveViewQuery,
mirrors how S2's rebuild.go inverts ResolveExpr). Then part (c) the engine
wiring (writeViewRewriteRow→OutRuleAction, RuleIsCanonical, relhasrules flip,
loadViewsFromHeap swap, test lock-ins) + standby-query E2E. See deferral_ledger
2026-07-19 M0123-S3 sub-slice 2 part (a) row.

In-flight: none.
