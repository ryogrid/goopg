(idle — nothing in flight)

Last landed: DU-002 slice 220 (loop #35) — GIN catalog-only index + `fastupdate`
boolean storage parameter round-trips through pg_dump.

What happened: TWO gaps. (1) GIN was not creatable — execCreateIndex routed only
gist/spgist to the catalog-only registration branch; every other non-btree method
fell through to "index method %q is not supported in v0". (2) the CREATE INDEX
WITH(…) parser discarded `fastupdate`. Reused slice 219's EXACT bool plumbing:
widened the catalog-only branch guard to `gist||spgist||gin` (GIN now registers
metadata-only — pg_class/pg_index/pg_get_indexdef populated, no physical build, no
acceleration/opclass enforcement, identical to the pre-existing gist path) and it
persists `idx.FastUpdate = s.FastUpdate`. Parser recognizes `fastupdate` via the
existing parseReloptionBool into CreateIndexStmt.FastUpdate (*bool tri-state).
Index.FastUpdate appended to reloptionList() after fillfactor/deduplicate_items;
BuildIndexDef already renders `USING <idx.Method>` so a GIN index dumps
`USING gin … WITH (fastupdate='off')`; pg_class.reloptions cell → `{fastupdate=off}`.
JSON-persisted; advisory catalog/dump-only (no GIN pending-list). Same normalization
limit as slice 219 (token → on/off; unrecognized silently ignored).

Files: internal/parser/ast.go (CreateIndexStmt.FastUpdate), internal/parser/ddl.go
(WITH-loop fastupdate capture), internal/catalog/catalog.go (Index.FastUpdate +
reloptionList()), internal/executor/operators_ddl.go (gin in catalog-only branch +
persist), internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestIndexFastUpdateSurfacesInPgClassReloptions), internal/testport/pgdump_connsetup_test.go
(foo_fastupdate_idx fixture + indexDefs assertion), docs/design/0110-0001-pg-dump-tap-port.md
(Slice 220), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; catalog+parser+FULL executor suites
PASS; TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: GIN/brin gain more catalog-only reloptions (gin `gin_pending_list_limit` int;
brin `autosummarize` bool / `pages_per_range` int — brin still needs the catalog-only
branch widened, same one-line change as gin here). Or the BIGGER `toast.*` namespace
(toast-table pg_class modeling; reltoastrelid hardcoded 0) / composite types.
