(idle — nothing in flight)

Last landed: DU-002 slice 182 (loop #150) — per-column storage override
(`ALTER COLUMN ... SET STORAGE {PLAIN|MAIN|EXTERNAL|EXTENDED}`) now round-trips through pg_dump.
Pivoted OFF the (closed) column-DEFAULT fall-through audit to a real pg_dump feature gap.

pg_dump's dumpTableSchema emits `ALTER TABLE ONLY <t> ALTER COLUMN <c> SET STORAGE <mode>;` only
when `a.attstorage != t.typstorage`. Parser accepted the clause + executor recorded
`catalog.Column.Storage`, but TWO layers dropped it:
  1. buildUserPGAttributeRow populated attstorage from the TYPE default unconditionally → always
     == typstorage → pg_dump never emitted. Fixed via new storageNameToAttCode helper
     (plain/main/external/extended → 'p'/'m'/'e'/'x') that shadows the type default when set.
  2. LOAD-BEARING: pg_attribute is a HEAP populated by syncTableToCatalogHeap at CREATE TABLE; the
     AlterTableSetStorage executor arm only mutated in-memory Column.Storage, never the heap row.
     Fixed by flushing through the same delete-old-rows + syncTableToCatalogHeap re-sync path
     DROP COLUMN / SET NOT NULL use (gated on catalogHeapSyncAvailable).
Dump-fidelity only (goopg doesn't TOAST). Verified: TestPort_PgDumpConnectionSetup PASS (3.02s).

Files: internal/executor/pg18_user_catalog_rows.go (+storageNameToAttCode, attstorage override),
internal/executor/operators_ddl.go (AlterTableSetStorage arm: heap re-sync),
internal/executor/pg18_user_catalog_rows_test.go (TestUserPGAttributeStorageOverride),
internal/testport/pgdump_connsetup_test.go (storcol fixture, 2 positive + 1 negative assert),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 182), .ralph/fix_plan.md (loop-150 PROGRESS).
Gates: gofmt OK; go vet executor+testport clean; full ./internal/executor/ PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next (slice 183 candidates): (1) column COMPRESSION dump fidelity — attcompression is the exact
analogous gap (pg_dump emits SET COMPRESSION when attcompression differs); parser currently
IGNORES the COMPRESSION keyword (ddl.go:2431) so it'd need a Column.Compression field + the same
heap-resync wiring. SAME shape as this slice, clean follow-up. (2) deferred MINVALUE/MAXVALUE
keyword-AST-node slice (HIGHER RISK: partition routing). (3) close validateDefaultExpr
array/row/CASE/InExpr recursion gap (executor semantic change — own gates).
