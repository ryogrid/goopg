Task: M0123-S3 sub-slice 2 part (b) — pgnodes view-query rebuild inverse.
COMPLETE + committed this loop (pure internal/pgnodes, no engine wiring).

Landed: `RebuildViewQuery(*Query) (*parser.SelectStmt, error)`
(rebuild_query.go) — reload-time inverse of ResolveViewQuery; IR Query → goopg
view AST. Self-describing (NO RelationResolver): FROM name = single RTE
eref.aliasname, column names = eref.colnames, so Var.varattno→colnames[attno-1].
Fixed point: resolve→Out…Read→RebuildViewQuery→resolve reproduces the input
Query byte-for-byte. rebuildTarget emits an explicit AS alias ONLY when resname
differs from the forward targetName auto-derivation (column/func name) — the
exact inverse — so plain column targets round-trip with no redundant alias.
Refactor (behavior-preserving): rebuild.go's rebuildOpExpr/rebuildFuncExpr made
recursion-injectable (rebuildOpExprWith/rebuildFuncExprWith(node, rec)) so the
query scope reuses identical opno→OpCode / funcid→proname reconstruction with a
Var-aware recursion; scalar path passes Rebuild unchanged.

Files: internal/pgnodes/rebuild_query.go (new), rebuild_query_test.go (new),
rebuild.go (extracted *With helpers), docs/design/0123-0004-*.md (retitled
sub-slices 1–2b + new §"Sub-slice 2b") + README index, .ralph/fix_plan.md
(S3 part (b) note) + deferral_ledger.md (new row).

Key symbols: pgnodes.RebuildViewQuery, viewRebuildScope (rebuildTarget/
rebuildExpr/naturalName/columnName), rebuildOpExprWith, rebuildFuncExprWith.

Gates run: go build ./... clean; go vet ./internal/pgnodes/ clean; gofmt -l on
my 3 files clean; go test ./internal/pgnodes/ GREEN incl. TestRebuildViewQuery
(2 live PG18.3 goldens, fixed-point) + Structure + Rejects(4). pgbench smoke via
pre-commit hook. ralph-state-guard OK (auto-repaired progress marker).

Next step (next loop): M0123-S3 sub-slice 2 part (c) — the ENGINE WIRING:
writeViewRewriteRow→OutRuleAction(ResolveViewQuery) when supported else SQL text;
set catalog.Table.RuleIsCanonical; flip pg18_user_catalog_rows.go:511 relhasrules
to read it (leave catalog.go virtual builders false); swap loadViewsFromHeap to
discriminate stored ev_action on leading '(' → ReadRuleAction→RebuildViewQuery
else parser.Parse (standalone-unconditional per NamespaceDBOid); update
relhasrules=false test lock-ins (pg_stat_wal_receiver_nailed_test.go:111-114,
e2e_failover_goopg_to_pg_test.go:278); then standby-query E2E. See ledger
2026-07-19 M0123-S3 sub-slice 2 part (b) row.

In-flight: none.
