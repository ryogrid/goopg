Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 22 (empty
pg_range virtual view) COMPLETE this loop. NOTHING in flight;
next loop starts on slice 23 (pg_event_trigger virtual view).

=== DONE (loop #45) — DU-002 slice 22 ===
getCasts runs `SELECT tableoid, oid, castsource, casttarget, castfunc,
castcontext, castmethod FROM pg_cast c WHERE NOT EXISTS ( SELECT 1 FROM
pg_range r WHERE c.castsource = r.rngtypid AND c.casttarget =
r.rngmultitypid ) ORDER BY 3,4` → aborted at `relation "pg_range" does
not exist`. Fix (catalog-only, mirrors slices 20/21):
- internal/catalog/catalog.go: added empty pg_range virtual view,
  OID 3541 (pg_range.h), beside pg_conversion. NOTE: pg_range has NO oid
  column; rngtypid is the key. Cols: rngtypid oid, rngsubtype oid,
  rngmultitypid oid, rngcollation oid, rngsubopc oid, rngcanonical
  regproc(oid), rngsubdiff regproc(oid). VirtualRows = nil. goopg defines
  no range types → NOT EXISTS always true → empty view gives identical
  dump — VERIFIED empirically.
- Design doc 0110-0001 slice-22 block added; pgdump_connsetup_test.go
  header updated (next blocker → pg_event_trigger); fix_plan loop #45 entry.
Gates: build/gofmt/vet clean; catalog suite PASS;
TestPort_PgDumpConnectionSetup PASS. tpch-spotcheck N/A (additive empty
virtual view; zero query-path/row-count risk).

=== NEXT STEP — DU-002 slice 23 (pg_event_trigger virtual view) ===
After slice 22, getCasts passes. pg_dump advances to getEventTriggers →
`relation "pg_event_trigger" does not exist` (VERIFIED empirically by
TestPort_PgDumpConnectionSetup). The query is:
  SELECT e.tableoid, e.oid, evtname, evtenabled, evtevent, evtowner,
  array_to_string(array(select quote_literal(x) from unnest(evttags) as
  t(x)), ', ') as evttags, e.evtfoid::regproc as evtfname FROM
  pg_event_trigger e ORDER BY e.oid
Add the pg_event_trigger virtual view in internal/catalog/catalog.go
(beside pg_range), OID 3466 (pg_event_trigger.h CATALOG(pg_event_trigger,
3466,...)). Cols (pg_event_trigger.h): oid, evtname name, evtevent name,
evtowner oid, evtfoid oid, evtenabled "char", evttags text[]. goopg
defines no event triggers → EMPTY view likely suffices — VERIFY
empirically. NOTE the query uses unnest(evttags) + array_to_string; with
0 rows that projection is never evaluated, so empty text[] col is fine.

ORTHOGONAL PRE-EXISTING (track separately, irrelevant to empty pg_dump views):
reading a text[] column back from the heap yields the BINARY array encoding
(KindString w/ raw bytes), not the text repr expandArrayDatum parses.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
