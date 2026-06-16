Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 59 COMPLETE
(committing this loop). NEXT loop starts on slice 60.
NOTHING in flight after commit.

=== DONE (loop #14) — DU-002 slice 59 (GENERATED STORED column round-trip) ===
Gap (confirmed via pg_dump.c read + passing assert): `area integer GENERATED
ALWAYS AS (w * h) STORED` dumped as a PLAIN `area integer` — the GENERATED
clause was silently dropped. pg_dump (dumpTableSchema) prints the clause only
when print_default is true, which requires BOTH pg_attribute.attgenerated='s'
AND a pg_attrdef row (tbinfo->attrdefs[j] != NULL); getTableAttrs forces
separate=false for generated cols so they stay inline. goopg already set
attgenerated='s' (attGeneratedFor) but atthasdef was `DefaultExpr != nil`
(false for a gen col) and pg_attrdef.VirtualRows only iterated DefaultExpr cols
→ no attrdef row.
FIX (2 sites):
  - internal/executor/pg18_user_catalog_rows.go buildUserPGAttributeRow:
    atthasdef = `col.DefaultExpr != nil || col.GeneratedExpr != ""`.
  - internal/catalog/catalog.go pg_attrdef VirtualRows: switch — DefaultExpr →
    formatExprForAttrdef; else GeneratedExpr → adbin=col.GeneratedExpr (verbatim,
    a col is never both). pg_get_expr passes adbin through, so clause has single
    parens `(w * h)` (PG may add normalizing parens; both restore equivalently).
Files: pg18_user_catalog_rows.go, catalog.go (pg_attrdef loop ~2790),
internal/testport/pgdump_connsetup_test.go (gen fixture + slice-59 asserts),
docs/design/0110-0001-pg-dump-tap-port.md (slice 59 bullet + guard paragraph).
Gates: gofmt clean; vet clean (catalog+executor); catalog+executor+parser suites
PASS; TestPort_PgDumpConnectionSetup PASS (exit-0, gen col round-trips);
pgbench CI-parity smoke via pre-commit hook.

=== NEXT STEP — DU-002 slice 60 ===
Enrich the fixture to find the next REAL pg_dump gap. Candidates still open:
  - IDENTITY column (GENERATED ... AS IDENTITY): attidentity hardcoded "" in
    buildUserPGAttributeRow (line ~301) → NOT round-tripped. BUT needs a backing
    SEQUENCE (sequences skipped from pg_class virtual view: Virtual && no View →
    getTables never sees relkind='S'). Larger slice — sequence support first.
  - SEQUENCE / serial column — same sequence-skip blocker.
  - MATERIALIZED VIEW round-trip (separate parser/AST path; no RawDef captured).
  - RECURSIVE view (WITH RECURSIVE …) — RawDef capture path may differ.
ALWAYS: add ONE fixture element, run TestPort_PgDumpConnectionSetup, inspect the
actual dump (temp t.Logf at the exit-0 return), confirm goopg doesn't already
handle it before assuming a gap. Known-working: CHECK, DEFAULT now(), typmods,
FKs, comments, ordered indexes, views, renamed-col views, GENERATED STORED cols.
Known orthogonal: plpgsql user funcs can't dump (plpgsql absent from pg_language).
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
