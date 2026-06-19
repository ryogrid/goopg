Task: DU-002 slice 272 (loop #39) — COMPLETE, ready to commit.

Last landed: a `NO INHERIT` NOT NULL on a STANDALONE (non-inherited) table
round-trips through pg_dump as the INLINE `<col> <type> NOT NULL NO INHERIT`
form. Slices 270/271 covered NOT NULL on *inherited* columns (standalone body
items); 272 exercises the same connoinherit='t' bit on a LOCAL column rendered
inline. NO production change — the whole path already existed but no dump had
asserted it (parser ddl.go:2475 → executor operators_ddl.go:2493 → catalog
builder catalog.go:3992). Verified vs real pg_dump 18.3:
`c integer NOT NULL NO INHERIT,\n    d integer`.

Files:
- internal/testport/pgdump_connsetup_test.go — nninh fixture
  (CREATE TABLE public.nninh (c integer NOT NULL NO INHERIT, d integer)) +
  body assert (`c integer NOT NULL NO INHERIT` present, `d integer` present).
- internal/parser/alter_test.go — TestParseCreateTableColumnNotNullNoInherit
  (inline trailer sets NotNullNoInherit; plain NOT NULL leaves it false).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 272 section + Next note.
- .ralph/fix_plan.md — slice 272 progress (loop #39).

Gates: gofmt clean; go build ./... clean; TestParseCreateTableColumnNotNullNoInherit
PASS; go test ./internal/parser/ ./internal/executor/ ./internal/catalog/ PASS;
TestPort_PgDumpConnectionSetup PASS (3.47s); pgbench pre-commit smoke (enforced
by .githooks/pre-commit on commit).

Next (slice 273+): a `NO INHERIT` NOT NULL added to a standalone table via
`ALTER TABLE ... ADD CONSTRAINT <name> NOT NULL <col> NO INHERIT` end-to-end
through a dump, or the named-differs inline variant
`c integer CONSTRAINT <name> NOT NULL NO INHERIT`.
