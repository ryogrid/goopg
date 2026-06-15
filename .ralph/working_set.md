Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 11 (pg_opfamily
virtual view) COMPLETE this loop. NOTHING in flight; next loop starts on slice 12
(pg_ts_parser view).

=== DONE (loop #34) — DU-002 slice 11 ===
pg_dump's getOpfamilies runs `SELECT tableoid, oid, opfmethod, opfname,
opfnamespace, opfowner FROM pg_opfamily`; aborted with `relation "pg_opfamily"
does not exist`. Fix:
- internal/catalog/catalog.go (beside pg_opclass): added empty pg_opfamily
  virtual view OID 2753, schema from pg_opfamily.h: oid, opfmethod oid, opfname
  name, opfnamespace oid, opfowner oid. VirtualRows returns nil. EMPTY is
  correct: getOpfamilies reads all operator families, filters system-defined at
  dump-out by namespace dumpability; built-ins live in pg_catalog (never
  dumped), goopg has no user operator families.
- pgdump_connsetup_test.go: updated slice-10→11 next-blocker comment to
  pg_ts_parser.
- design doc 0110-0001 slice-11 block + fix_plan loop #34 entry.
Gates run: go build ./... OK; gofmt clean; go vet catalog OK; catalog + initdb
unit suites PASS; TestPort_PgDumpConnectionSetup PASS (getOpfamilies completes).
tpch-spotcheck N/A (additive empty virtual view; zero query-path/row-count risk).

=== NEXT STEP — DU-002 slice 12 (pg_ts_parser virtual view) ===
TestPort_PgDumpConnectionSetup now fails in getTSParsers with
`relation "pg_ts_parser" does not exist`. Query:
  SELECT tableoid, oid, prsname, prsnamespace, prsstart::oid, prstoken::oid,
  prsend::oid, prsheadline::oid, prslextype::oid FROM pg_ts_parser
Slice 12 = add an empty pg_ts_parser virtual view (OID 3601) beside pg_opfamily
in catalog.go. Schema (pg_ts_parser.h): oid, prsname name, prsnamespace oid,
prsstart regproc(=oid), prstoken regproc, prsend regproc, prsheadline regproc,
prslextype regproc. The ::oid casts resolve no-op since regproc is oid-compat.
EMPTY correct: built-in TS parsers in pg_catalog, filtered out by namespace
dumpability; only user TS parsers dumped (none here). Then continue getter
battery (getTSDictionaries pg_ts_dict, getTSTemplates pg_ts_template,
getTSConfigurations pg_ts_config, getForeignDataWrappers, getForeignServers…).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
