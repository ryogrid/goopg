(idle — nothing in flight)

Last landed: DU-002 slice 221 (loop #36) — GIN index `gin_pending_list_limit`
integer storage parameter round-trips through pg_dump. First INDEX-level *integer*
reloption beyond fillfactor.

What happened: GIN already registers catalog-only (slice 220); only gap was the
WITH parser discarding every key except fillfactor/deduplicate_items/fastupdate.
Reused fillfactor's exact int plumbing (slice 218): parser reads the int via
parseIntLit into CreateIndexStmt.GinPendingListLimit (int, 0=unset); executor
range-validates like PG (<64 || >2097151 → SQLSTATE 22023, mirroring reloptions.c
min 64 / max MAX_KILOBYTES) next to the fillfactor check, persists
idx.GinPendingListLimit in the catalog-only GIN branch. Index.GinPendingListLimit
(strconv.Itoa) appended to reloptionList() AFTER fastupdate, so combining renders
the stable {fastupdate=off,gin_pending_list_limit=2048} order. BuildIndexDef dumps
`USING gin … WITH (gin_pending_list_limit='128')`. JSON-persisted; advisory
catalog/dump-only (no GIN pending list). 0 sentinel keeps a plain GIN index
byte-identical.

Files: internal/parser/ast.go (CreateIndexStmt.GinPendingListLimit),
internal/parser/ddl.go (WITH-loop capture), internal/catalog/catalog.go
(Index.GinPendingListLimit + reloptionList()), internal/executor/operators_ddl.go
(range-validate + persist), internal/executor/operators_fillfactor_reloptions_test.go
(NEW TestIndexGinPendingListLimitSurfacesInPgClassReloptions +
TestIndexGinPendingListLimitOutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (foo_ginlimit_idx fixture + indexDefs
assertion), docs/design/0110-0001-pg-dump-tap-port.md (Slice 221), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; FULL executor suite PASS;
parser+catalog PASS; TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke
on commit.

Next: brin `autosummarize` bool / `pages_per_range` int — brin still needs the
catalog-only branch widened to `gist||spgist||gin||brin` (one-line change, same as
gin in slice 220). Or the BIGGER `toast.*` namespace (toast-table pg_class
modeling; reltoastrelid hardcoded 0) / composite types.
