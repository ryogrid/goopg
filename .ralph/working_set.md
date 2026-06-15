Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 13 (pg_ts_template
virtual view) COMPLETE this loop. NOTHING in flight; next loop starts on slice 14
(pg_ts_dict virtual view).

=== DONE (loop #36) — DU-002 slice 13 ===
pg_dump's getTSTemplates runs `SELECT tableoid, oid, tmplname, tmplnamespace,
tmplinit::oid, tmpllexize::oid FROM pg_ts_template`; aborted with
`relation "pg_ts_template" does not exist`. Fix:
- internal/catalog/catalog.go (beside pg_ts_parser): added empty pg_ts_template
  virtual view OID 3764, schema from pg_ts_template.h: oid, tmplname name,
  tmplnamespace oid, tmplinit/tmpllexize regproc. VirtualRows returns nil.
  ::oid casts no-op (regproc oid-compat). EMPTY correct: built-in TS templates
  in pg_catalog (never dumped), goopg has no user TS templates.
- pgdump_connsetup_test.go: added slice-13 to landed list; CORRECTED the bogus
  "pg_ts_dict already passes" note; updated next-blocker comment to
  getTSDictionaries/pg_ts_dict.
- design doc 0110-0001 slice-13 block + fix_plan loop #36 entry.
Gates run: go build ./... OK; gofmt clean; go vet catalog OK; catalog + initdb
unit suites PASS; TestPort_PgDumpConnectionSetup PASS (getTSTemplates completes).
tpch-spotcheck N/A (additive empty virtual view; zero query-path/row-count risk).

IMPORTANT correction: loop #35's claim that pg_ts_dict already passed was WRONG.
getTSDictionaries runs AFTER getTSTemplates in pg_dump's getter battery, so the
dump aborted at getTSTemplates before reaching pg_ts_dict. pg_ts_dict has a
pg_class entry seeded by initdb (OID 3600) BUT goopg's query layer resolves
system catalogs via the in-memory virtual-view registry, not the on-disk heap —
so it is NOT queryable and needs a virtual view like the other slices.

=== NEXT STEP — DU-002 slice 14 (pg_ts_dict virtual view) ===
TestPort_PgDumpConnectionSetup now logs next blocker getTSDictionaries:
`relation "pg_ts_dict" does not exist`. Query:
  SELECT tableoid, oid, dictname, dictnamespace, dictowner, dicttemplate,
  dictinitoption FROM pg_ts_dict
Slice 14 = add an empty pg_ts_dict virtual view beside pg_ts_template in
catalog.go. Schema (pg_ts_dict.h, OID 3600): oid, dictname name, dictnamespace
oid, dictowner oid, dicttemplate oid (regproc? NO — it's a regdictionary/oid ref
to pg_ts_template; use oid), dictinitoption text. VERIFY exact types via
pg_ts_dict.h before coding. EMPTY correct: built-in TS dicts in pg_catalog,
filtered by namespace dumpability; only user TS dicts dumped (none here). Then
continue getter battery (getTSConfigurations pg_ts_config, getForeignDataWrappers
pg_foreign_data_wrapper…). RUN the test after each add to find the REAL next
blocker rather than trusting the predicted one (slice 12's prediction was wrong).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
