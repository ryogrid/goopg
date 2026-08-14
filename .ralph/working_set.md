(row 1355 closed — M0119-0006 continues)

**This loop (2026-08-14):** closed deferral-ledger row 1355 (regtype schema-gap)
in two slices. Slice A (`f4d594d3`, 88th) added `NamespaceOID` to the four
user-type registries (enum/domain/composite/range), populated at CREATE
TYPE/DOMAIN (CREATE AGGREGATE schema-with-public-fallback pattern) and at
WAL/startup reload (pg_type typnamespace col 2, previously dropped). Slice B
(`3a03e18e`, 89th) made `userTypeNameForOID`/`RegtypeName` take a per-schema
`qualify func(schema) bool` (was fixed bool), added `Catalog.SchemaNameForOID`,
and made all ten user-type arms render `regOutQualified(schema, name,
qualify(schema))` with `"[]"`/multirange kept outside quoting; `regOutShared`
dropped `regtypeQualify`. The `::regtype` cast / `format_type()` / walsender /
RAISE now render an off-path non-public type as `schema.name` + quoted.

**Key symbols:** `userTypeNameForOID`/`RegtypeName`/`regOutQualifySchema`
(expr.go), `regOutShared`/`RegOut`/`RegOutArgVisible`/`RegOutRendererVisible`
(reg_identifier.go), `Catalog.SchemaNameForOID` + `NamespaceOID` on
EnumType/Domain/CompositeType/RangeType (catalog.go), reloadUser*FromHeap
(catalog_heap_reload.go). Tests: `regtype_actual_schema_test.go`,
`TestCreateTypeDomainRecordsNamespaceOID`.

**Residual (filed):** new ledger row — the SELECT wire / COPY TO / `reg*`→text
cast / array-element paths still pass a constant `!publicSchemaVisible` predicate
(dispatch.go:3227, copy.go:90, expr.go:3381 regCastQualify, codec_array.go:335),
so an off-path non-public type renders BARE there under the default search_path,
now divergent from the corrected cast path. Row-1339 family; thread a per-schema
predicate through those four sites.

**Remaining M0119-0006 (rough order of narrowness):**
1. row 1351 — bare-`char` arg OID: arglist carries only arg Name; carry resolved
   OID per arg (catalog-representation change).
2. rows 1340/1344 — mixed-case role folding / arg-type case (case-folding family).
3. whole-db (unscoped) pg_amcheck — blocked on index AMs, STORAGE EXTERNAL
   TOAST, box/int4range/int4[] HEAP types, multi-DB orchestration.

**Next step:** row 1351 (bare-`char` arg OID) — delegate a `researcher` to scope
it first: which arglist carriers need the resolved OID, where each is populated,
and the exact regprocedure-arglist render sites. Banner ranks M0119 first
(M-NIGHTLY has no open items; the 2 regress/enum+oid nightly items were already
filed+fixed by an earlier slice this loop).

**Gates run:** go build PASS; executor/server/wal/catalog/plpgsql suites PASS;
5 targeted regtype tests PASS (uncached); tpch-spotcheck PASS (Q12=2/Q13=35);
pre-commit units PASS; pre-commit pgbench smoke PASS (hook); full
TestPort_RegressSuite PASS (47 PASS/185 SKIP/0 FAIL, all 41 must-pass green).

**Delegation:** implementer (slice B) + tester (package gates, long gates,
regress) + reviewer (adversarial) all DONE. Handoff:
`tmp/ralph-handoffs/0119-0006-regtype-schema-qualify/`.

**In-flight:** none.
