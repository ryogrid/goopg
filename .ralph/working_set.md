Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 23
(pg_event_trigger view + correlated unnest() arg fix) COMPLETE this loop.
NOTHING in flight; next loop starts on slice 24 (broaden pg_attribute/
pg_constraint/pg_type columns for getTableAttrs).

=== DONE (loop #47) — DU-002 slice 23 ===
getEventTriggers: `SELECT e.tableoid, e.oid, evtname, evtenabled, evtevent,
evtowner, array_to_string(array(select quote_literal(x) from unnest(evttags)
as t(x)), ', ') as evttags, e.evtfoid::regproc as evtfname FROM
pg_event_trigger e ORDER BY e.oid`. Two gaps fixed together:
- (a) internal/catalog/catalog.go: added empty pg_event_trigger virtual view,
  OID 3466 (pg_event_trigger.h), beside pg_range. Cols: oid, evtname name,
  evtevent name, evtowner oid, evtfoid oid, evtenabled "char", evttags text[].
  VirtualRows = nil. (This was already added by the crashed loop #46.)
- (b) internal/planner/planner.go planFromUnnest: was the SAME correlated
  FROM-clause SRF arg bug as slice 18 (pg_options_to_table), but for unnest.
  It built `ctx := &resolveContext{}; if lateralCtx != nil { ctx = lateralCtx }`
  — never chaining up to planParent, so `unnest(evttags)` inside the correlated
  array(...) subquery failed `column "evttags" does not exist`. Fix mirrors
  planPgOptionsToTable/planGenerateSeries: ctx := &resolveContext{parent:
  planParent}; copy-and-reparent lateral siblings when parent==nil.
- Guard: internal/planner/unnest_correlated_test.go TestPlanUnnestCorrelatedArg
  (ARRAY/scalar/LATERAL forms). Design doc 0110-0001 slice-23 block added;
  pgdump_connsetup_test.go header updated (next blocker → getTableAttrs); fix_plan
  loop #47 entry.
Gates: build/gofmt/vet clean; catalog + planner suites PASS;
TestPlanUnnestCorrelatedArg PASS; TestPort_PgDumpConnectionSetup PASS.
tpch-spotcheck N/A (additive empty view + correlated-arg resolution only on a
previously-failing FROM-SRF path; zero existing-query row-count risk).

=== NEXT STEP — DU-002 slice 24 (getTableAttrs catalog columns) ===
After slice 23, pg_dump advances into the per-table attribute dump and fails:
`column a.attstattarget does not exist`. The getTableAttrs query reads many
pg_attribute/pg_constraint/pg_type columns goopg's views do not expose:
attstattarget, attstorage, attfdwoptions, attcompression, attidentity,
atthasmissing, attmissingval, attgenerated, conislocal, convalidated,
connoinherit, conkey, ... Broaden those catalog columns. This is a DEEPER
slice than the empty-view additions — likely multiple sub-slices, one missing
column (or small group) at a time, re-running TestPort_PgDumpConnectionSetup
to find the next missing column empirically.

ORTHOGONAL PRE-EXISTING (track separately): reading a text[] column back from
the heap yields the BINARY array encoding (KindString w/ raw bytes), not the
text repr expandArrayDatum parses. Irrelevant to empty pg_dump views.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
