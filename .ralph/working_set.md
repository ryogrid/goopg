Task: M0123-S3 sub-slice 2 part (c) — pgnodes view-query ENGINE WIRING.
COMPLETE + committed this loop.

Landed: canonical pg_rewrite ev_action end-to-end for plain views.
- catalog.Table.RuleIsCanonical (catalog.go) — HARD-coupled to relhasrules.
- executor/sys_pg_rewrite.go: viewRelationResolver (pgnodes.RelationResolver
  over live catalog), viewColumnCanonicalType (reads atttypid/typmod/collation
  back out of buildUserPGAttributeRow so Var vartype == standby pg_attribute),
  canonicalViewEvAction (resolve→OutRuleAction bytes | SQL-text fallback),
  relkindByteForTable. writeViewRewriteRow now takes a pre-resolved ev_action.
- operators_ddl.go syncTableToCatalogHeap: resolves + sets RuleIsCanonical
  BEFORE buildUserPGClassRow (load-bearing — pg_class heap row is the standby's
  relhasrules source; late flag → standby saw false → 42809).
- relhasrules reads the flag in the heap row (pg18_user_catalog_rows.go:511)
  AND the virtual builder (catalog.go:6978); system rows stay false.
- initdb/catalog_heap_reload.go: rebuildViewFromEvAction discriminates leading
  "({" → ReadRuleAction→RebuildViewQuery (restores flag) else parser.Parse.

Gates (all GREEN): executor TestViewColumnCanonicalType/TestViewAttrIndexConstants;
initdb TestRebuildViewFromEvAction; testport TestPort_ViewsSurviveRestart
(relhasrules=true survives restart); testport TestE2E_FailoverGoopgToPG — a real
PG18 standby reports relhasrules=t AND pg_get_viewdef PARSES the canonical
ev_action via stringToNode + deparses it back to the exact SELECT (adversarial
byte-compat proof). build/vet clean; initdb full suite green (111s).

Key symbols: canonicalViewEvAction, viewRelationResolver, viewColumnCanonicalType,
rebuildViewFromEvAction, catalog.Table.RuleIsCanonical.

DEFERRED (ledger 2026-07-19): ROW-LEVEL standby eval — `SELECT * FROM v` on the
promoted standby still 42809. Diagnosed: relhasrules=t + rule row present +
index 2620 present + pg_get_viewdef WORKS, but the executor's rewriter uses the
relcache rule lock (rd_rules), not pg_rewrite; copied pg_internal.init caches a
ruleless relcache entry. Parallels pg_attrdef_standby_consumption_blocked.

Next step (next loop): M0123-S4 coverage/hardening (numeric/timestamptz datums,
CASE/BoolExpr/NullTest targets, byte-diff oracle) OR the rd_rules standby-eval
unblock (see ledger resume point: relcache.c RelationBuildRuleLock +
pg_internal.init rule serialization / exclude user views from copied init).

In-flight: none.
