Task: M0123-S2 sub-slice 2 parts (b)(c) — canonical pg_attrdef.adbin writer +
reload wiring. COMPLETE + committed this loop.

Landed: `pgnodes.ResolveForColumn(e,targetType)→(Node,bool)` (exact-type-match
gate) drives `canonicalAttrdefText` in the `writeAttrdefRow` funnel; reload
`rebuildAttrdefExpr` discriminates on leading `{`→Read/Rebuild else ParseExpr.
adbin stored as plain string (nodeToString is pure ASCII — no NewBytesDatum).

Files: internal/pgnodes/resolver_expr.go (+ResolveForColumn) & _test.go,
internal/executor/sys_pg_attrdef.go (+canonicalAttrdefText) & operators_ddl.go
(caller) & sys_pg_attrdef_test.go (new), internal/initdb/catalog_heap_reload.go
(+rebuildAttrdefExpr) & catalog_heap_reload_attrdef_test.go (new),
internal/testport/e2e_failover_goopg_to_pg_test.go (deferral comment only),
docs/design/0123-0003-*.md (new) + 0123-0002-*.md + README, fix_plan + ledger.

Gates run: go build ./... clean; go vet clean; internal/pgnodes GREEN;
TestCanonicalAttrdefText GREEN; TestRebuildAttrdefExpr GREEN; full internal/initdb
(105s) GREEN; TestE2E_FailoverGoopgToPG GREEN; ralph-state-guard OK (auto-repair);
pgbench smoke via pre-commit hook.

DISCOVERED / DEFERRED (ledger 2026-07-19, both orthogonal to node-tree serde):
 (1) standby-EVAL E2E blocked by pg_attrdef catalog completeness — real PG18
     standby can't build a usable pg_attrdef tupledesc from goopg's streamed
     pg_attribute (relid 2604 lacks usable `adbin` col: `column "adbin" does not
     exist`) AND AttrDefaultFetch opens the unmaterialized adrelid/adnum index
     (OID 2656: `could not open relation with OID 2656`). Fix = bootstrap
     pg_attribute completion + materialize 2656/2657 index files, THEN re-add the
     standby INSERT DEFAULT VALUES assertion to e2e_failover_goopg_to_pg_test.go.
 (2) canonical stxexprs blocked on a List IR node (stxexprs is `(...)` List of
     trees) — arrives with S3/S4.

Next step (next loop): M0123-S3 — resolver_query.go (SelectStmt→Query IR) +
Query/RTE/Var out/read/rebuild + writeViewRewriteRow canonical ev_action +
RuleIsCanonical flag; OR pick the deferred pg_attrdef-index materialization to
unblock the standby-eval gate. S3 is the fix_plan's next unchecked M0123 item.

In-flight: none.
