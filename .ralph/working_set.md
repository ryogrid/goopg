(idle — nothing in flight)

Last landed: DU-002 slice 194 (loop #7) — VIRTUAL vs STORED generated-column
strategy round-trips through pg_dump.

What happened: PG18 admits `GENERATED ALWAYS AS (expr) [STORED|VIRTUAL]` (default
VIRTUAL); pg_dump keys on `pg_attribute.attgenerated` ('s'→`… STORED`,
'v'→bare `GENERATED ALWAYS AS (expr)`). goopg parsed both keywords but discarded
the choice — `attGeneratedFor` was hardcoded "s", so VIRTUAL dumped as STORED.
Fix: new `catalog.Column.GeneratedVirtual` (parser sets STORED→false,
VIRTUAL/bare→true per PG18 default), threaded through both CREATE TABLE column
paths in operators_ddl.go (+ cleared under INCLUDING GENERATED), mapped in
attGeneratedFor → 'v'/'s'. Shared atthasdef/pg_attrdef expr wiring (slice 59)
feeds both. goopg still MATERIALIZES every generated column on write (STORED
storage semantics); GeneratedVirtual is catalog/dump-only — runtime unchanged.
True compute-on-read VIRTUAL is a separate larger feature.

Files: internal/catalog/catalog.go (Column.GeneratedVirtual), internal/parser/
ast.go + ddl.go (~L2589), internal/parser/gen_override_test.go
(TestGeneratedColumnStorageStrategy), internal/executor/operators_ddl.go (2 copy
sites + INCLUDING GENERATED clear), internal/executor/pg18_user_catalog_rows.go
(attGeneratedFor), pg18_user_catalog_rows_test.go (TestAttGeneratedForStorageStrategy),
internal/testport/pgdump_connsetup_test.go (genv VIRTUAL fixture + no-STORED
assertion), docs/design/0110-0001-pg-dump-tap-port.md (Slice 194), fix_plan.md.

Gates: gofmt OK; go build ./... clean; go vet testport clean; parser + catalog +
executor PASS; TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next (slice 195 candidates): (1) composite types (CREATE TYPE AS) — larger,
pg_class.reltype hardcoded 0 (pg18_user_catalog_rows.go:453). (2) remaining
partition-child trailers — none obvious after USING/WITH/ON COMMIT/TABLESPACE.
(3) per-column attfdwoptions (foreign-table only, NULL today).
