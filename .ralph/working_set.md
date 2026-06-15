Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 14 (pg_ts_dict
virtual view) COMPLETE this loop. NOTHING in flight; next loop starts on slice 15
(pg_ts_config virtual view).

=== DONE (loop #37) — DU-002 slice 14 ===
pg_dump's getTSDictionaries runs `SELECT tableoid, oid, dictname, dictnamespace,
dictowner, dicttemplate, dictinitoption FROM pg_ts_dict`; aborted with
`relation "pg_ts_dict" does not exist`. Fix:
- internal/catalog/catalog.go (beside pg_ts_template): added empty pg_ts_dict
  virtual view OID 3600, schema from pg_ts_dict.h: oid, dictname name,
  dictnamespace oid, dictowner oid, dicttemplate oid (FK to pg_ts_template — NOT
  regproc), dictinitoption text. VirtualRows returns nil. EMPTY correct: built-in
  TS dicts in pg_catalog (never dumped), goopg has no user TS dicts.
- pgdump_connsetup_test.go: added slice-14 to landed list; updated next-blocker
  comment to getTSConfigurations/pg_ts_config.
- design doc 0110-0001 slice-14 block + fix_plan loop #37 entry.
Gates run: go build ./... OK; gofmt clean; go vet catalog OK; catalog + initdb
unit suites PASS; TestPort_PgDumpConnectionSetup PASS (getTSDictionaries completes).
tpch-spotcheck N/A (additive empty virtual view; zero query-path/row-count risk).

=== NEXT STEP — DU-002 slice 15 (pg_ts_config virtual view) ===
TestPort_PgDumpConnectionSetup now logs next blocker getTSConfigurations:
`relation "pg_ts_config" does not exist`. Query:
  SELECT tableoid, oid, cfgname, cfgnamespace, cfgowner, cfgparser FROM pg_ts_config
Slice 15 = add an empty pg_ts_config virtual view beside pg_ts_dict in
catalog.go. Schema (pg_ts_config.h, OID 3602): oid, cfgname name, cfgnamespace
oid, cfgowner oid, cfgparser oid (FK to pg_ts_parser). VERIFY exact types via
pg_ts_config.h before coding. EMPTY correct: built-in TS configs in pg_catalog,
filtered by namespace dumpability; only user TS configs dumped (none here). Then
continue getter battery (getForeignDataWrappers pg_foreign_data_wrapper,
getForeignServers pg_foreign_server…). RUN the test after each add to find the
REAL next blocker rather than trusting the predicted one.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
