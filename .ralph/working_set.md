Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 12 (pg_ts_parser
virtual view) COMPLETE this loop. NOTHING in flight; next loop starts on slice 13
(pg_ts_template view).

=== DONE (loop #35) — DU-002 slice 12 ===
pg_dump's getTSParsers runs `SELECT tableoid, oid, prsname, prsnamespace,
prsstart::oid, prstoken::oid, prsend::oid, prsheadline::oid, prslextype::oid
FROM pg_ts_parser`; aborted with `relation "pg_ts_parser" does not exist`. Fix:
- internal/catalog/catalog.go (beside pg_opfamily): added empty pg_ts_parser
  virtual view OID 3601, schema from pg_ts_parser.h: oid, prsname name,
  prsnamespace oid, prsstart/prstoken/prsend/prsheadline/prslextype regproc.
  VirtualRows returns nil. ::oid casts no-op (regproc oid-compat). EMPTY correct:
  built-in TS parsers in pg_catalog (never dumped), goopg has no user TS parsers.
- pgdump_connsetup_test.go: added slice-12 to landed list; updated next-blocker
  comment to getTSTemplates/pg_ts_template (NOT pg_ts_dict — see below).
- design doc 0110-0001 slice-12 block + fix_plan loop #35 entry.
Gates run: go build ./... OK; gofmt clean; go vet catalog OK; catalog + initdb
unit suites PASS; TestPort_PgDumpConnectionSetup PASS (getTSParsers completes).
tpch-spotcheck N/A (additive empty virtual view; zero query-path/row-count risk).

IMPORTANT discovery: after adding pg_ts_parser, the test advanced PAST
getTSDictionaries too — pg_ts_dict already exists as a real NAILED on-disk
catalog seeded by initdb (internal/initdb/*), not a virtual view. So no view was
needed for it. The genuine next blocker is getTSTemplates.

=== NEXT STEP — DU-002 slice 13 (pg_ts_template virtual view) ===
TestPort_PgDumpConnectionSetup now fails in getTSTemplates with
`relation "pg_ts_template" does not exist`. Query:
  SELECT tableoid, oid, tmplname, tmplnamespace, tmplinit::oid, tmpllexize::oid
  FROM pg_ts_template
Slice 13 = add an empty pg_ts_template virtual view beside pg_ts_parser in
catalog.go. Schema (pg_ts_template.h): oid, tmplname name, tmplnamespace oid,
tmplinit regproc, tmpllexize regproc. OID = 3764 (verify via pg_ts_template.h).
::oid casts no-op. EMPTY correct: built-in TS templates in pg_catalog, filtered
out by namespace dumpability; only user TS templates dumped (none here). Then
continue getter battery (getTSConfigurations pg_ts_config, getForeignDataWrappers
pg_foreign_data_wrapper, getForeignServers pg_foreign_server…). NOTE: some of
these may already exist as nailed catalogs (like pg_ts_dict) — run the test after
each add to find the REAL next blocker rather than trusting the predicted one.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
