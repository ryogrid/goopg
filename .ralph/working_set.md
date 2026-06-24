Loop #21: M0118-0008 — `reindex-concurrently-toast` (LAST unpromoted spec; other
24 pass strict). Landed TOAST-exposure epic SLICE 3 (design 0118-0086). Spec
stays `defer` (steps 4–5 of the 5-step epic remain).

## What landed (slice 3 of 5)
The TOAST relation's auto-created UNIQUE btree index `pg_toast_<oid>_index`
(on chunk_id, chunk_seq) is now exposed in BOTH pg_class (relkind='i') and
pg_index, so the spec setup's join
`SELECT indexrelid::regclass::text FROM pg_index WHERE indrelid =
(SELECT oid FROM pg_class WHERE relname=<toast rel>)` resolves the index name.
Catalog-only — goopg has no real TOAST index (catalog/regclass-only).
- NEW const `toastIndexOidOffset = 200_000_000` (idx OID = parent OID + 200M;
  100M above toastRelidOffset so [200M,300M) never overlaps [100M,200M)).
- `ToastRelName(oid)` now resolves the index range FIRST → `pg_toast.pg_toast_
  <oid>_index`; expr.go regclass arm already falls through to it.
- pg_class builder emits the relkind='i' row (relnamespace=99, relam=403,
  relnatts=2) inside the existing `if hasToastRel` block; toast-rel row's
  relhasindex flips f→t.
- pg_index builder emits one toast-index row per toast-bearing table
  (indexrelid=OID+200M, indrelid=OID+100M, unique, indkey="1 2").
- NEW `(*InMemory).toastBearingTables()` — enumerates the SAME table set the
  pg_class TOAST emission uses (shared `tableHasToastRelation` gate) so pg_class
  and pg_index can't diverge into an indexrelid with no pg_class row.

Files: internal/catalog/catalog.go (toastIndexOidOffset, ToastRelName index arm,
toastBearingTables, pg_class toast-index row + relhasindex flip, pg_index
toast-index rows); internal/executor/toast_relation_exposure_test.go (NEW
TestToastRelationIndexExposed); docs/design/0118-0086-*.md + README; ledger.

## Next step (slice 4)
`ALTER TABLE/INDEX … RENAME` on a pg_toast relation/index under
allow_system_table_mods (setup renames pg_toast_<oid>→reind_con_toast and its
_index→reind_con_toast_idx) + `pg_toast.<newname>` name resolution. The synthetic
toast rows live only in the virtual builder, not c.tables — RENAME must record an
override map (old synthetic OID → new name) consulted by both the virtual builder
AND name→OID lookup. Then slice 5 = REINDEX {TABLE,INDEX} CONCURRENTLY
pg_toast.<name> routing (rides 0118-0029 waitForRelationLockers). Re-run
blast-radius parity suites after each.

## Gates run (this loop)
go test ./internal/{catalog,executor}/ PASS; TestToastRelationIndexExposed +
TestToastRelationAutoExposed + TestReltoastrelidRegclassRendersToastName PASS;
ALL blast-radius parity suites PASS (PgDump001Basic, PgDumpConnectionSetup,
PgAmcheck* incl. AllTables/002/BtreeIndexCheck whole-DB walks, Scripts*);
IsolationReindexConcurrently + IsolationPlpgsqlToast PASS; build/gofmt clean;
pgbench smoke = pre-commit hook.
