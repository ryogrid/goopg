(idle — nothing in flight)

Last landed: DU-002 slice 222 (loop #37) — BRIN catalog-only index +
`pages_per_range` integer storage parameter round-trips through pg_dump.

What happened: Unlike GIN, BRIN was previously *rejected* by CREATE INDEX
(`index method "brin" is not supported in v0`), so this slice first widened the
catalog-only branch guard from `gist||spgist||gin` to `gist||spgist||gin||brin`
(BRIN now registers catalog metadata only — no physical storage/summarization).
Then mirrored slice 221's int plumbing on the BRIN key: parser reads the int via
parseIntLit into CreateIndexStmt.PagesPerRange (int, 0=unset); executor
range-validates like PG (<1 || >131072 → SQLSTATE 22023, reloptions.c min 1 / max
BRIN_MAX_PAGES_PER_RANGE=131072) next to the gin_pending_list_limit check;
persists idx.PagesPerRange in the catalog-only branch. Index.PagesPerRange
(strconv.Itoa) appended to reloptionList() after gin_pending_list_limit;
BuildIndexDef dumps `USING brin … WITH (pages_per_range='64')`. JSON-persisted;
advisory catalog/dump-only (no BRIN summarization). 0 sentinel keeps a plain BRIN
index byte-identical.

Files: internal/parser/ast.go (CreateIndexStmt.PagesPerRange),
internal/parser/ddl.go (WITH-loop capture), internal/catalog/catalog.go
(Index.PagesPerRange + reloptionList()), internal/executor/operators_ddl.go
(branch widen + range-validate + persist),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestIndexPagesPerRangeSurfacesInPgClassReloptions +
TestIndexPagesPerRangeOutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (foo_brinrange_idx fixture + indexDefs
assertion), docs/design/0110-0001-pg-dump-tap-port.md (Slice 222), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; FULL executor suite PASS;
parser+catalog PASS; TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit
smoke on commit.

Next: brin `autosummarize` bool — the remaining BRIN reloption; same bool
plumbing as fastupdate/deduplicate_items (parseReloptionBool → *bool tri-state,
reloptionList renders on/off). Or the BIGGER toast.* namespace (toast-table
pg_class modeling; reltoastrelid hardcoded 0) / composite types.
