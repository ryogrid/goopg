Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 15 (pg_ts_config
virtual view) COMPLETE this loop. NOTHING in flight; next loop starts on slice 16
(pg_foreign_data_wrapper virtual view).

=== DONE (loop #38) — DU-002 slice 15 ===
pg_dump's getTSConfigurations runs `SELECT tableoid, oid, cfgname, cfgnamespace,
cfgowner, cfgparser FROM pg_ts_config`; aborted with `relation "pg_ts_config"
does not exist`. Fix:
- internal/catalog/catalog.go (beside pg_ts_dict): added empty pg_ts_config
  virtual view OID 3602, schema from pg_ts_config.h: oid, cfgname name,
  cfgnamespace oid, cfgowner oid, cfgparser oid (FK to pg_ts_parser).
  VirtualRows returns nil. EMPTY correct: built-in TS configs in pg_catalog
  (never dumped), goopg has no user TS configs.
- pgdump_connsetup_test.go: added slice-15 to landed list; updated next-blocker
  comment to getForeignDataWrappers/pg_foreign_data_wrapper (confirmed).
- design doc 0110-0001 slice-15 block + fix_plan loop #38 entry.
Gates run: go build ./... OK; gofmt clean; go vet catalog OK; catalog + initdb
unit suites PASS; TestPort_PgDumpConnectionSetup PASS (getTSConfigurations
completes). tpch-spotcheck N/A (additive empty virtual view; zero
query-path/row-count risk).

=== NEXT STEP — DU-002 slice 16 (pg_foreign_data_wrapper virtual view) ===
TestPort_PgDumpConnectionSetup now logs next blocker getForeignDataWrappers:
`relation "pg_foreign_data_wrapper" does not exist`. Query:
  SELECT tableoid, oid, fdwname, fdwowner, fdwhandler::pg_catalog.regproc,
  fdwvalidator::pg_catalog.regproc, fdwacl, acldefault('F', fdwowner) AS
  acldefault, array_to_string(ARRAY(SELECT quote_ident(option_name) || ' ' ||
  quote_literal(option_value) FROM pg_options_to_table(fdwoptions) ORDER BY
  option_name), E',\n    ') AS fdwoptions FROM pg_foreign_data_wrapper
Slice 16 = add an empty pg_foreign_data_wrapper virtual view beside pg_ts_config
in catalog.go. Schema (pg_foreign_data_wrapper.h, OID 2328): oid, fdwname name,
fdwowner oid, fdwhandler oid, fdwvalidator oid, fdwacl aclitem[], fdwoptions
text[]. VERIFY exact types via pg_foreign_data_wrapper.h before coding. EMPTY
correct: goopg has no FDWs by default; only user FDWs dumped (none here). NOTE:
the query also uses pg_options_to_table(fdwoptions) — verify that SRF exists in
goopg; if not, with an empty view the ARRAY subquery is never evaluated, so it
may be fine, but RUN the test to confirm. Then getForeignServers
(pg_foreign_server), getUserMappings (pg_user_mappings)… RUN the test after each
add to find the REAL next blocker rather than trusting the predicted one.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
