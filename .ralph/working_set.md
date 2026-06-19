(idle — nothing in flight)

Last landed: DU-002 slice 219 (loop #34) — index `deduplicate_items` btree BOOLEAN
storage parameter round-trips through pg_dump. goopg's FIRST index-level boolean
reloption (slice 218's fillfactor was an int).

What happened: goopg's CREATE INDEX `WITH (…)` parser only extracted `fillfactor`,
discarding every other key — so `deduplicate_items=off` was silently lost (index
restored with btree dedup implicitly ON). Threaded through: parser (ddl.go) recognizes
`deduplicate_items`, parses bool via NEW `parseReloptionBool` (PG parse_bool token set
on/off/true/false/yes/no/1/0); value → `CreateIndexStmt.DeduplicateItems` (*bool
tri-state, nil=unset). execCreateIndex persists `idx.DeduplicateItems = s.DeduplicateItems`
in BOTH branches (btree guard now includes `s.DeduplicateItems != nil`). KEY refactor
(sibling-path law): both render surfaces now share a NEW `Index.reloptionList()` helper
returning ordered key=value pairs (fillfactor first) — (1) BuildIndexDef emits multi-option
` WITH (fillfactor='70', deduplicate_items='off')` (single-quoted, joined ", "); (2) index
pg_class.reloptions cell renders `{fillfactor=70,deduplicate_items=off}`. JSON-persisted.
Limitation: bool normalized to on/off (false/no/0 → off); unrecognized token silently
ignored (matches fillfactor leniency).

Files: internal/parser/ast.go (CreateIndexStmt.DeduplicateItems), internal/parser/ddl.go
(WITH capture + parseReloptionBool helper), internal/catalog/catalog.go
(Index.DeduplicateItems + reloptionList() + both render surfaces),
internal/executor/operators_ddl.go (persist both branches),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestIndexDeduplicateItemsSurfacesInPgClassReloptions), internal/testport/pgdump_connsetup_test.go
(foo_dedup_idx fixture + indexDefs assertion), docs/design/0110-0001-pg-dump-tap-port.md
(Slice 219), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; parser+catalog+executor suites PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: more btree boolean reloptions reuse this exact plumbing (gin `fastupdate`,
`buffering` for gist). Or the BIGGER `toast.*` namespace (needs toast-table pg_class
modeling — reltoastrelid hardcoded 0) or composite types (CREATE TYPE AS; reltype 0).
