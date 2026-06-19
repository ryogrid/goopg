(idle — nothing in flight)

Last landed: DU-002 slice 218 (loop #33) — index `fillfactor` storage parameter
round-trips through pg_dump. goopg's FIRST index-level reloption (slices 54–217 were
all table/heap reloptions).

What happened: parser already captured `CreateIndexStmt.Fillfactor` and `execCreateIndex`
already range-validated (10–100 → 22023), but it was NEVER persisted to catalog.Index, so
the dump silently dropped it. Persisted via `idx.Fillfactor = s.Fillfactor` in BOTH
execCreateIndex branches (btree post-create block — guard now includes `s.Fillfactor != 0`;
and gist/spgist branch). KEY insight (sibling-path law): a plain CREATE INDEX dumps via
`pg_get_indexdef`/`BuildIndexDef` (pg_dump emits indexdef verbatim, pg_dump.c:18133), NOT
via the pg_class.reloptions/indreloptions column — that column (pg_dump.c:7775) is only used
by the CONSTRAINT-backed index path (pg_dump.c:18459). So I updated BOTH: BuildIndexDef
emits ` WITH (fillfactor='N')` after NULLS NOT DISTINCT, before WHERE (ruleutils.c order;
flatten_reloptions single-quotes); AND the index pg_class row (relkind 'i', col 32) renders
`{fillfactor=N}`. catalog.Index.Fillfactor is JSON-persisted (survives reload).
Limitation: parser uses 0 as "unset" sentinel → `fillfactor=0` reads as unspecified (PG min 10).

Files: internal/catalog/catalog.go (Index.Fillfactor field + index pg_class reloptions render
+ BuildIndexDef WITH clause), internal/executor/operators_ddl.go (persist in both branches),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestIndexFillfactorSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (foo_ff_idx fixture + indexDefs assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 218), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; catalog+executor+parser suites PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: index `deduplicate_items` (btree boolean reloption — another clean index-level slice,
same plumbing as fillfactor but boolean + BuildIndexDef multi-option WITH handling). Or the
BIGGER `toast.*` namespace (needs toast-table pg_class modeling — reltoastrelid hardcoded 0)
or composite types (CREATE TYPE AS; pg_class.reltype hardcoded 0).
