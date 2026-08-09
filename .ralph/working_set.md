Task: M0119-0004 DU-002 — fixed `column "pid" does not exist` in pg_dump INHERITS restore (FIXED)

Files:
- internal/executor/operators_ddl.go: changed parent-not-found from silent skip to 42P01 error; added writeInheritsDependRow call after table creation
- internal/executor/sys_pg_depend.go: added writeInheritsDependRow function (NORMAL dep row for INHERITS)
- internal/catalog/catalog.go: added INHERITS dependency rows to PGDependRowsForDBOid virtual view

Key symbols:
- writeInheritsDependRow: writes pg_depend heap row (pg_class, childOID, 0) → (pg_class, parentOID, 0) deptype='n'
- PGDependRowsForDBOid: now iterates c.ns(dbOid).tables and adds rows for each InheritsParentOID
- execCreateTable: parent lookup failure now returns 42P01 error (matching PostgreSQL)

Hypothesis/Findings:
- Root cause: pg_dump output `CREATE TABLE idfa_child INHERITS (idfa_parent)` BEFORE `CREATE TABLE idfa_parent` because goopg's pg_depend virtual view had no INHERITS dependency rows. pg_dump's topological sort didn't know idfa_child depends on idfa_parent, so it sorted by OID (child had lower OID).
- Fix 1: Added INHERITS NORMAL ('n') dependency rows to PGDependRowsForDBOid → pg_dump now outputs parents before children.
- Fix 2: Changed execCreateTable to error on nonexistent parent (was silently skipping → child created without inherited columns → later ALTER TABLE failed with confusing "column does not exist").
- Fix 3: Added writeInheritsDependRow for heap durability (complements the virtual view).
- Verified: error changed from "column pid does not exist" → "relation ichk_parent does not exist" → syntax error on DEFERRABLE (next DU-002 blocker — different gap entirely).

Next step: Next DU-002 blocker is DEFERRABLE INITIALLY DEFERRED syntax support for UNIQUE constraints. See TestPort_PgDumpConnectionSetup output.

Gates run:
- go build ./...: PASS
- go test ./internal/catalog/... ./internal/executor/... ./internal/parser/... ./internal/server/...: PASS
- RALPH_PRECOMMIT_SCOPE=units: PASS (all packages)
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, all 3 workloads)
- TestPort_PgDumpConnectionSetup: PASS (next = DEFERRABLE syntax)
- make ralph-state-guard: REPAIRED + OK

In-flight: none
