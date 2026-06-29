(idle — nothing in flight)

Last loop (#50): M0119-0004 **CONSTRAINT TRIGGER round-trip in pg_dump**
(DU-002 slice 327) — LANDED, committed.

pg_get_triggerdef_worker (ruleutils.c) renders `CREATE CONSTRAINT TRIGGER … AFTER
<ev> ON <nsp>.<rel> [NOT ]DEFERRABLE INITIALLY {IMMEDIATE|DEFERRED} FOR EACH ROW
…` — CONSTRAINT prefix gated on a valid tgconstraint, deferrability clause always
spelled out in full. goopg's `CREATE CONSTRAINT TRIGGER` was a DEAD parse branch
(matched via acceptIdentKeyword but CONSTRAINT is a reserved keyword token), so it
failed to parse. Fix (dump-fidelity only — no deferred firing):
- parser ast.go/ddl.go: CreateTriggerStmt.{IsConstraint,Deferrable,InitDeferred};
  parseCreateTriggerTail(pos, isConstraint); CONSTRAINT case now matches
  KwConstraint keyword token; reuses parseConstraintDeferrable after ON table.
- catalog.go: Trigger.{IsConstraint,Deferrable,InitDeferred,ConstraintOID};
  pg_trigger projects non-zero tgconstraint + tgdeferrable/tginitdeferred.
- operators_ddl.go: execCreateTrigger copies flags + allocs ConstraintOID.
- expr.go: buildTriggerDefString emits CREATE CONSTRAINT TRIGGER + deferrability
  clause after the ON-table name.

Files: internal/parser/{ast.go,ddl.go,create_trigger_test.go},
internal/catalog/catalog.go, internal/executor/{operators_ddl.go,expr.go,
triggerdef_test.go}, internal/testport/pgdump_connsetup_test.go (trg_cdef +
trg_cdfr fixtures+asserts), docs/design/0119-0004-constraint-trigger-pgdump.md
(+README 0119-0004ad).

Gates: parser/catalog/executor suites PASS; TestPort_PgDumpConnectionSetup PASS
(real pg_dump 18.3, byte-identical); go build/vet clean; pgbench smoke via
pre-commit hook.

NEXT loop — remaining M0119-0004 trigger getter gaps. Best contained candidate:
`REFERENCING … OLD/NEW TABLE AS` transition tables (tgoldtable/tgnewtable — just
identifiers, no expr deparse; emitted before FOR EACH ROW). Then trigger
`WHEN (condition)` (tgqual) — but that needs an OLD/NEW-qualified expression
deparser (formatExprForAttrdef DROPS qualifiers, so NEW.b would render as just b;
a dedicated trigger-WHEN deparser keeping lowercased new./old. is required).
