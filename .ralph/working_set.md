Task: DU-002 slice 271 (loop #38) — COMPLETE, ready to commit.

Last landed: a *named* NOT NULL on an INHERITED column via PG18's
`ALTER TABLE ... ADD CONSTRAINT <name> NOT NULL <col>` round-trips through
pg_dump as the `CONSTRAINT <name> NOT NULL <col>` body form (named counterpart
of slice 270's unnamed "" path). pg_dump prints the CONSTRAINT prefix only when
conname differs from the computed default `<tbl>_<col>_not_null` (pg_dump.c:9926
vs :17228). NEW production support: the `ADD CONSTRAINT ... NOT NULL <col>`
shape was previously unparseable.

Output (verified vs real pg_dump 18.3):
`CREATE TABLE public.idfnn_child (\n    CONSTRAINT idfnn_nn NOT NULL pid,\n
    extra integer\n) INHERITS (public.idfnn_parent);`

Files:
- internal/parser/ast.go — AlterTableAddNotNull kind + NoInherit field.
- internal/parser/ddl.go — `NOT NULL` case in ALTER TABLE ADD switch
  (consumes `NOT NULL <col> [NO INHERIT]`).
- internal/executor/operators_ddl.go — AlterTableAddNotNull arm (set
  Column.NotNull, AddNotNull with explicit name / auto-name fallback,
  idempotent, 42703 missing col, heap re-sync).
- internal/parser/alter_test.go — TestParseAlterTableAddNotNull.
- internal/executor/operators_ddl_named_check_test.go — TestAlterTableAddNotNullNamed.
- internal/testport/pgdump_connsetup_test.go — idfnn_parent/idfnn_child fixture
  + ADD CONSTRAINT idfnn_nn NOT NULL pid + body assert (CONSTRAINT idfnn_nn NOT
  NULL pid present, inherited cols absent) + INHERITS assert.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 271 section + Next note.
- .ralph/fix_plan.md — slice 271 progress (loop #38).

Gates: gofmt clean; go build ./... clean; TestParseAlterTableAddNotNull PASS;
TestAlterTableAddNotNullNamed PASS; go test ./internal/parser/ ./internal/executor/
./internal/catalog/ PASS; TestPort_PgDumpConnectionSetup PASS (3.80s); pgbench
pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 272+): a `NO INHERIT` NOT NULL on a STANDALONE (non-inherited) table
dumped inline as `<col> <type> NOT NULL NO INHERIT` — exercises the
connoinherit='t' rendering the new noInherit arg now threads but no dump path
has yet asserted.
