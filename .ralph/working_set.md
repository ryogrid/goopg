Task: M0110-0003 (AC-003 pg_amcheck) — loop #11. Ported the whole-database
relation-enumeration tier + REFUTED the previously-assumed blocker #3.

=== WHAT LANDED (this loop) ===
003_check.pl's clean-db path runs the *default* pg_amcheck (no scoping), which
enumerates every checkable relation and dispatches verify_heapam/bt_index_check
per relation — a tier distinct from the single-`--table` path. New
TestPort_PgAmcheckAllTables (internal/testport/pgamcheck_alltables_port_test.go)
drives the real binary over a goopg DB mixing the relkinds 003_check builds that
goopg supports (heap table, btree indexes incl. UNIQUE, sequence, view, matview)
in user schema s1. --schema s1 run = clean (exit 0); unscoped whole-database run
(reaches pg_catalog.*) = ALSO clean (exit 0).

KEY FINDING: blocker #3 ("system-catalog heap resolution") is NOT a real blocker.
goopg never feeds its system catalogs to pg_amcheck's heap-check dispatch, so the
default whole-db run is clean with no verify_heapam-on-catalog gap. Prior
working_set/fix_plan/design-doc claims corrected.

Files: internal/testport/pgamcheck_alltables_port_test.go (NEW, self-promoting +
hard-asserts blocker #1/#2 non-regression), docs/design/0110-0003 (blocker #3
correction + enumeration-tier section), CSV AC-003 rationale + regenerated md,
.ralph/fix_plan.md (loop #11 progress note).

Gates: pg_amcheck port suite PASS (001/002/004 + btree + alltables, 7.2s);
TestPort_PgAmcheckAllTables PASS clean (not skipped); gofmt + go vet
./internal/testport clean. TPC-H spotcheck SKIPPED (no data dir; test-only,
zero TPC-H surface).

=== NEXT STEP (resume) — AC-003 remainder ===
003_check.pl proper now blocked ONLY on feature/corruption work (no catalog-heap
gap): (a) hash/gist/gin/brin/spgist index AMs goopg lacks (s5 relations), (b)
box/int4range/int4[] column types, (c) STORAGE EXTERNAL TOAST corruption, (d)
multi-database (db1/db2/db3) orchestration, (e) file-removal/first-page-overwrite
corruption mechanics + per-relation expected reports. Each is multi-milestone.
005_opclass_damage = CREATE OPERATOR CLASS + pg_amproc parity. AC-003 stays
`defer`. The clean enumeration/dispatch tier goopg CAN do is now fully covered.
