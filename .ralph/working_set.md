(idle — nothing in flight)

Last landed: DU-002 slice 223 (loop #38) — BRIN index `autosummarize` boolean
storage parameter round-trips through pg_dump. The remaining BRIN reloption.

What happened: Mirrored slice 220's (fastupdate) `*bool` tri-state plumbing on a
BRIN key. Parser (ddl.go WITH-loop) recognizes `autosummarize`, reads the bool via
parseReloptionBool into CreateIndexStmt.AutoSummarize (*bool, nil=unset). Executor
persists idx.AutoSummarize in the catalog-only branch (no range check — bool token
already validated by parser). Index.AutoSummarize (rendered on/off) appended to
reloptionList() AFTER pages_per_range (PG relopt order); BuildIndexDef dumps
`USING brin … WITH (autosummarize='on')`. JSON-persisted; advisory catalog/dump-only.
nil sentinel (PG default off) keeps a plain BRIN index byte-identical.

Files: internal/parser/ast.go (CreateIndexStmt.AutoSummarize), internal/parser/ddl.go
(WITH-loop bool capture), internal/catalog/catalog.go (Index.AutoSummarize +
reloptionList()), internal/executor/operators_ddl.go (persist in catalog-only branch),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestIndexAutoSummarizeSurfacesInPgClassReloptions +
TestIndexPagesPerRangeAndAutoSummarizeCombined),
internal/testport/pgdump_connsetup_test.go (foo_brinauto_idx fixture + indexDefs
assertion), docs/design/0110-0001-pg-dump-tap-port.md (Slice 223), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; parser+catalog PASS; executor
reloption suite PASS; TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke
on commit.

Next: All simple index/table reloptions now land (fillfactor, deduplicate_items,
fastupdate, gin_pending_list_limit, pages_per_range, autosummarize). The remaining
pg_dump-fidelity work is BIGGER: toast.* reloption namespace (needs toast-table
pg_class modeling; reltoastrelid hardcoded 0) or composite types. Pick one as a
multi-slice effort, not a one-line mirror.
