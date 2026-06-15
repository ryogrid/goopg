Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 21 (empty
pg_conversion virtual view) COMPLETE this loop. NOTHING in flight;
next loop starts on slice 22 (pg_range virtual view).

=== DONE (loop #44) — DU-002 slice 21 ===
getConversions runs `SELECT tableoid, oid, conname, connamespace, conowner
FROM pg_conversion` → aborted at `relation "pg_conversion" does not exist`.
Fix (catalog-only, mirrors slice 20):
- internal/catalog/catalog.go: added empty pg_conversion virtual view,
  OID 2607 (pg_conversion.h), beside pg_default_acl. Cols: oid, conname
  name, connamespace oid, conowner oid, conforencoding int4, contoencoding
  int4, conproc oid, condefault bool. VirtualRows = nil. Although PG ships
  ~130 built-in conversions, all are in pg_catalog and filtered out at
  dump-out time (selectDumpableObject → DUMP_COMPONENT_NONE), so the EMPTY
  view gives an identical dump — VERIFIED empirically.
- Design doc 0110-0001 slice-21 block added; pgdump_connsetup_test.go
  header updated (next blocker → pg_range); fix_plan loop #44 entry.
Gates: build/gofmt/vet clean; catalog suite PASS;
TestPort_PgDumpConnectionSetup PASS. tpch-spotcheck N/A (additive empty
virtual view; zero query-path/row-count risk).

=== NEXT STEP — DU-002 slice 22 (pg_range virtual view) ===
After slice 21, getConversions passes. pg_dump advances to getCasts →
`relation "pg_range" does not exist` (VERIFIED empirically by
TestPort_PgDumpConnectionSetup). The query is:
  SELECT tableoid, oid, castsource, casttarget, castfunc, castcontext,
  castmethod FROM pg_cast c WHERE NOT EXISTS ( SELECT 1 FROM pg_range r
  WHERE c.castsource = r.rngtypid AND c.casttarget = r.rngmultitypid )
  ORDER BY 3,4
pg_cast already EXISTS; the NOT EXISTS subquery references pg_range, which
does not. Add the pg_range virtual view in internal/catalog/catalog.go
(beside pg_conversion), OID 3541 (pg_range.h CATALOG(pg_range,3541,...)).
Cols (pg_range.h): rngtypid oid, rngsubtype oid, rngmultitypid oid,
rngcollation oid, rngsubopc oid, rngcanonical regproc/oid, rngsubdiff
regproc/oid. NOTE: pg_range has NO oid column (rngtypid is the key). goopg
defines no range types → EMPTY view likely suffices — VERIFY empirically.

ORTHOGONAL PRE-EXISTING (track separately, irrelevant to empty pg_dump views):
reading a text[] column back from the heap yields the BINARY array encoding
(KindString w/ raw bytes), not the text repr expandArrayDatum parses.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
