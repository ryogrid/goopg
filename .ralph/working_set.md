(idle — nothing in flight)

Last loop (#49): M0119-0004 **column-specific `UPDATE OF col1, col2` trigger
round-trip in pg_dump** (DU-002 slice 326) — LANDED, committed.

pg_dump's getTriggers emits pg_get_triggerdef verbatim; ruleutils.c
pg_get_triggerdef_worker appends ` OF <cols>` after the UPDATE event (from
pg_trigger.tgattr). Slice 319 made basic CREATE TRIGGER round-trip but skipped
the column-list form: parseCreateTriggerTail consumed only bare `UPDATE`, so the
`OF` token tripped the event loop (mistaken for `ON <table>`), the deparser had
no list, and tgattr was hard-coded empty. Fix (dump-fidelity only — fireTriggers
ignores the column restriction):
- parser ast.go/ddl.go: CreateTriggerStmt.UpdateColumns; UPDATE arm now accepts
  optional `OF col1, col2` (KwOf already exists for INSTEAD OF).
- catalog.go: Trigger.UpdateColumns + triggerUpdateColAttrs(tbl,cols) → tgattr
  (space-separated int2vector of 1-based attnums, like pg_index.indkey).
- operators_ddl.go: execCreateTrigger copies s.UpdateColumns (re-aligned literal).
- expr.go: buildTriggerDefString emits ` OF c1, c2` after UPDATE via pgQuoteIdent.

Files: internal/parser/{ast.go,ddl.go,create_trigger_test.go},
internal/catalog/catalog.go, internal/executor/{operators_ddl.go,expr.go,
triggerdef_test.go}, internal/testport/pgdump_connsetup_test.go (trig_t gained
column b + trg_uof fixture+assert), docs/design/0119-0004-trigger-update-of-columns.md
(+README 0119-0004ac).

Gates: parser/catalog/executor suites PASS; TestPort_PgDumpConnectionSetup PASS
(real pg_dump 18.3, byte-identical); go build/vet clean; pgbench smoke via
pre-commit hook.

NEXT loop — remaining M0119-0004 pg_dump getter gaps. Contained candidates:
richer trigger forms — CREATE TRIGGER `WHEN (condition)` (tgqual via pg_get_expr,
the deparser already exists for policies/CHECK), then `REFERENCING … OLD/NEW
TABLE` (tgoldtable/tgnewtable), then CONSTRAINT triggers (tgdeferrable/
tginitdeferred + `DEFERRABLE`). The big enablers (per-role OID registry for
GRANT/ACL + named-role policies) still need the ARRAY(SELECT…)/array_to_string/
quote_ident query stack goopg lacks (Effort-L).
