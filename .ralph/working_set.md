(idle — nothing in flight)

Last landed: DU-002 slice 191 (loop #4) — per-leaf-partition storage parameters
(`WITH (fillfactor=N)`) round-trip through pg_dump.

What happened: PG allows storage params on a leaf partition; pg_dump re-emits
them on the leaf's own CREATE TABLE as `WITH (fillfactor='N')`. goopg persisted
the option only on the non-partition CREATE TABLE path (slice 54). The
partition-child path early-returned in BOTH twins:
- Parser (internal/parser/ddl.go): partition-child arm returned after FOR
  VALUES/PARTITION BY without scanning a trailing WITH → syntax error at WITH.
  Added a WITH parse (parseWithOptions) populating stmt.With.
- Executor (execCreatePartitionChild): never read s.With. Mirrored the main
  path — reject mixed-case names (42000), reject storage params on a
  sub-partitioned child (0A000), bounds-check fillfactor 10–100 (22023), persist
  via tbl.Fillfactor (shared pg_class.reloptions cell surfaces it).
Fixture: pfo LIST parent + pfo_1 WITH (fillfactor=70) + option-less pfo_2.
Assertion scopes the WITH (fillfactor='70') match to the pfo_1 statement (bare
match would also catch slice 54's opt table) and checks pfo_2 has no WITH.

Files: internal/parser/ddl.go, internal/parser/gen_override_test.go (2 tests),
internal/executor/operators_ddl.go, internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md (Slice 191), .ralph/fix_plan.md.
Gates: gofmt OK; go build ./... clean; parser + executor + TestPort_PgDumpConnectionSetup
PASS; pgbench pre-commit smoke on commit.

Next (slice 192 candidates): (1) partition-child TABLESPACE clause round-trip.
(2) composite types (CREATE TYPE AS) — larger, pg_class.reltype hardcoded 0
(pg18_user_catalog_rows.go:453). (3) MINVALUE/MAXVALUE keyword-AST-node — AVOID
(refactor of working partition-routing code).
