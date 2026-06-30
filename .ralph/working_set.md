(idle — nothing in flight)

Loop #92 COMPLETE: M0119-0004 DU-002 slice 361 — a `CREATE INDEX … USING hash`
index now dumps `USING hash (col)` instead of the B-tree substrate's
`USING btree`. goopg has no native hash AM: a hash index routes through
createBTreeIndex (catalog.Index.Method stays "btree"; only DeclaredHash records
the declared method, design 0118-0099). Fix = catalog.BuildIndexDef renders
"hash" when idx.DeclaredHash, mirroring pg_get_indexdef_worker's `USING %s`
(pg_am.amname). Byte-verified vs a throwaway PG 18.3 cluster. Committed.

Files: internal/catalog/catalog.go (BuildIndexDef DeclaredHash gate),
internal/catalog/index_def_hash_test.go (new unit test),
internal/testport/pgdump_connsetup_test.go (slice 361 DDL + indexDefs assert),
docs/design/0110-0001-pg-dump-tap-port.md (slice 361 entry), deferral ledger.

Deferred (ledgered): restart persistence — DeclaredHash is in-memory only, so a
re-loaded hash index after a server restart would dump `USING btree` again
(pg_am/pg_index access-method not durably persisted; same shared-catalog
runtime-write gap as the other restart-durability slices).

Gates run: catalog unit PASS; TestPort_PgDumpConnectionSetup PASS (4.9s);
go build ./... clean; pgbench smoke = pre-commit hook.

Next loop: pick a fresh M0119-0004 pg_dump slice. Candidate next surfaces:
the deferred slice-360 (a) typed string-literal cast inside a function-arg key
(needs operand-type threading); action-command / DO ALSO CREATE RULE forms
(full query reverse-compiler — milestone-sized); reserved-keyword-named-role
quoting; or another catalog-view gap.
