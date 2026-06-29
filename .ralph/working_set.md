(idle — nothing in flight)

Last loop (#41): M0119-0004 **CREATE TRIGGER round-trip in pg_dump** (DU-002
slice 319) — LANDED. Design `0119-0004-trigger-roundtrip.md`. Real feature gap
(not a guard): user triggers were silently dropped from pg_dump.

Three gaps fixed (the relhastriggers one was the load-bearing surprise):
1. `pg_class.relhastriggers` hardcoded `'f'` → pg_dump's getTriggers
   (pg_dump.c:8523) only adds a table's OID to its tbloids probe array when
   `tbinfo->hastriggers`; so the table was NEVER queried against pg_trigger.
   Now projects `'t'` when `len(t.Triggers)>0` in the VIRTUAL pg_class builder
   (pg_class.VirtualRows, catalog.go ~3758 — the one pg_dump reads, NOT the heap
   builder; sibling 'f' literals for sequences/views left untouched).
2. `pg_trigger.VirtualRows` returned nil → now one row/trigger (tgtype bitmask,
   tgfoid via Routines().LookupByName, tgenabled='O', tgisinternal='f',
   tgparentid=0).
3. `pg_get_triggerdef` registered in pg_proc but unimplemented → new
   `evalFuncCall` case (expr.go) + `buildTriggerDefString` mirroring ruleutils.c
   pg_get_triggerdef_worker. New `catalog.Trigger.OID` via AllocOID in
   execCreateTrigger (operators_ddl.go).

Files: internal/catalog/catalog.go (Trigger.OID, relhastriggers, pg_trigger
VirtualRows), internal/executor/operators_ddl.go (AllocOID), internal/executor/
expr.go (case + buildTriggerDefString), internal/executor/triggerdef_test.go,
internal/testport/pgdump_connsetup_test.go (slice 319 fixture+assert).

Gates: TestBuildTriggerDefString + TestPort_PgDumpConnectionSetup PASS vs real
pg_dump 18.3; catalog + executor suites PASS; go build clean; pgbench smoke =
pre-commit hook; ralph-state-guard pending.

NEXT loop — next pg_dump getter-battery gap. Uncovered features found this loop
(grep of pgdump_connsetup_test.go): CREATE RULE (pg_rewrite/pg_get_ruledef),
GRANT/ACL (relacl), CREATE POLICY / ROW LEVEL SECURITY (pg_policy), CLUSTER ON
(pg_index.indisclustered). Pick one as a real feature gap. Richer trigger forms
(WHEN/REFERENCING/UPDATE OF/CONSTRAINT) need parser+catalog fields first.
