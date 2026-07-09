Task: M0122-0007 4e — `CREATE DATABASE ... TEMPLATE` real relation-copy
mechanism (follow-up 22 landed a bounded validation-only slice; commit
875f9f37). This IS the epic's final remaining item.

Files (this loop, already committed): internal/server/database_ddl.go,
internal/server/database_ddl_test.go,
docs/design/0122-0018-per-database-catalog-namespace.md,
docs/design/README.md, .ralph/fix_plan.md, .ralph/deferral_ledger.md.

Key symbols: `resolveCreateDatabaseTemplate` (database_ddl.go) — currently
errors 0A000 on a non-empty template unless it aliases
`catalog.DefaultDBOid`. Replace its FeatureNotSupported branch with the real
copy; keep the existence check + the DefaultDBOid-skip (load-bearing, see
below). `createDatabasePhysicalDirectory` — currently only creates an empty
`base/<oid>/PG_VERSION` scaffold, never copies files.

Findings (confirmed this loop, don't re-derive):
- Physical storage IS real per-database: `internal/storage/smgr.go`
  `relDir`/`sharedOrPerDBRelDir` route every RelFileNode through
  `base/<dbOid>/<relOid>` (+ `_fsm`/`_vm`/`_init` forks).
- Catalog IS real per-database: `catalog.InMemory.namespaces
  map[uint32]*tableNamespace` (tables/indexes/byTable), accessed via
  `ns(dbOid)`/`getOrCreateNS(dbOid)`. Views AND sequences are just Table
  entries (IsSequence bool) in the SAME map — no separate registry to copy.
- `"template1"` and `"postgres"` BOTH alias `catalog.DefaultDBOid` (1) — a
  pre-existing legacy dual-mirror every fixture/pre-4c path writes real
  tables into. A copy mechanism must NOT treat DefaultDBOid as a normal
  template source (whose "content" is ambiguous between template1's own data
  and postgres's own data) — this is why the validation slice special-cases
  it. `template0` is oid 4, genuinely distinct, always empty.
- OIDs are cluster-wide (one `nextOID` counter) — copied objects need FRESH
  OIDs, not the source's OIDs (would collide). FK targets, index byTable
  keys etc. all need remapping to the new OIDs.
- pg_attribute/pg_type are heap-backed (see `pg_attribute_alter_needs_heap_resync`
  memory) — a copied table's columns need a heap re-sync write too, not just
  an in-memory Table struct copy.
- Residual gap found live (NOT this task, not fixed): a connection to ANY
  freshly CREATE DATABASE'd database can't query pg_class at all (`ERROR:
  relation "pg_class" does not exist") — system-catalog virtual builders
  aren't wired to a per-connection dbOid yet. Recorded in deferral ledger.
  Worth fixing before/alongside the copy mechanism since a copied database
  would be equally useless if you can't \d it afterward.

- A background Explore agent (see "In-flight" below) independently confirmed
  the above and added one new detail: enum types, domains, composite types,
  and routines are NOT namespace-scoped at all in `catalog.InMemory` (they
  live in separate global/DefaultDBOid-only sibling maps, e.g. `enumTypes`,
  `domains` — distinct from `tableNamespace`). The design doc already scopes
  these out of the whole epic (docs/design/0122-0018-...md L1043-ish), so the
  copy mechanism should realistically cover only tables/indexes/views/
  sequences (the namespace-scoped kinds) as its first cut — copying a table
  with a domain-typed column would still reference the (correctly shared,
  un-namespaced) domain definition, which needs no copying.

Next step: design doc's "Remaining 4e work" section
(docs/design/0122-0018-per-database-catalog-namespace.md) has the sketch.
Start by deciding whether to fix the pg_class-under-fresh-db gap FIRST (small,
independently valuable, makes the copy mechanism actually usable end-to-end)
or in the same loop as the copy mechanism.

Gates run: go build/vet clean; go test ./internal/server/... ./internal/catalog/...
./internal/executor/... ./internal/wal/... PASS; full repo -short PASS;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke PASS (0 failed, all 3
workloads, ran twice — once standalone, once as the commit's pre-commit
hook). make ralph-state-guard OK (self-repaired a benign status/progress
timestamp mismatch from the previous loop's clean exit).

In-flight: none. The background Explore agent (id a36d34e0c12dc6687) launched
early this loop reported back (after this loop's work was already committed)
— folded its one new finding (enum/domain/composite/routine scope-out) into
the notes above; nothing it found contradicted or required changes to the
committed work.
