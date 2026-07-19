Task: M0123-S3 sub-slice 1 — pure `internal/pgnodes` query-tree CODEC (no wiring).
COMPLETE + committed this loop.

Landed: IR nodes Query/RangeTblEntry/RTEPermissionInfo/FromExpr/RangeTblRef/
TargetEntry/Var/Alias (ir_query.go) + 2 new wire primitives — Bitmapset
`(b ...)` and String value node `"col"` (quoted, via outNode T_String path) vs
bare WRITE_STRING_FIELD via a faithful outToken port. outfuncs_query.go emits the
full ~45-field Query skeleton (fixed fields = view defaults) in outfuncs.c order +
OutRuleAction outer `(...)` ev_action wrapper. readfuncs_query.go = inverse AND
shape gate (readQuery validates every fixed field; readRangeTblEntry rejects
non-RTE_RELATION/tablesample/securityQuals → clean error = "keep SQL text").

Files: internal/pgnodes/ir_query.go (new), outfuncs_query.go (new),
readfuncs_query.go (new), query_roundtrip_test.go (new), outfuncs.go (+8 dispatch
cases), readfuncs.go (+8 dispatch cases), docs/design/0123-0004-*.md (new) +
README index, .ralph/fix_plan.md (S3 sub-slice-1 note) + deferral_ledger.md.

Key symbols: pgnodes.OutRuleAction / ReadRuleAction (ev_action list wrapper),
Query/RangeTblEntry/RTEPermissionInfo/FromExpr/RangeTblRef/TargetEntry/Var/Alias,
Bitmapset, outToken/unToken, wBitmapset/readBitmapsetField, wStringList.

Gates run: go build ./... clean; go vet ./internal/pgnodes/ clean; gofmt -l = only
pre-existing resolver_expr_test.go (version mismatch, not mine); go test
./internal/pgnodes/ GREEN incl. TestRuleActionRoundTrip (2 live PG18.3 goldens,
byte-for-byte) + TestRuleActionStructure + TestRuleActionShapeGate; pgbench smoke
via pre-commit hook.

Next step (next loop): M0123-S3 sub-slice 2 — the WIRING. (a) resolver_query.go
(*parser.SelectStmt + catalog → IR Query; compute varno/varattno, selectedCols
offset attno-FirstLowInvalidHeapAttributeNumber=-7, resorigtbl, perminfoindex);
(b) rebuild.go Query→goopg view AST; (c) wire writeViewRewriteRow→OutRuleAction,
set catalog.Table.RuleIsCanonical, flip pg18_user_catalog_rows.go:511 relhasrules,
swap loadViewsFromHeap, update relhasrules=false test lock-ins
(pg_stat_wal_receiver_nailed_test.go:111-114, e2e_failover_goopg_to_pg_test.go:278);
(d) gate: standby-query E2E (goopg CREATE VIEW + PG18 standby SELECT * FROM v ==
goopg's own rows). See deferral_ledger 2026-07-19 M0123-S3 sub-slice 1 row.

In-flight: none.
