Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 10 (pg_opclass
virtual view) COMPLETE this loop. NOTHING in flight; next loop starts on slice 11
(pg_opfamily view).

=== DONE (loop #33) — DU-002 slice 10 ===
pg_dump's getOpclasses runs `SELECT tableoid, oid, opcmethod, opcname,
opcnamespace, opcowner FROM pg_opclass`; aborted with `relation "pg_opclass"
does not exist`. Fix:
- internal/catalog/catalog.go (beside pg_operator): added empty pg_opclass
  virtual view OID 2616, schema from pg_opclass.h: oid, opcmethod oid, opcname
  name, opcnamespace oid, opcowner oid, opcfamily oid, opcintype oid, opcdefault
  bool, opckeytype oid. VirtualRows returns nil. EMPTY is correct: getOpclasses
  reads all operator classes, filters system-defined at dump-out by namespace
  dumpability; built-ins live in pg_catalog (never dumped), goopg has no user
  operator classes.
- pgdump_connsetup_test.go: updated slice-9→10 next-blocker comment to pg_opfamily.
- design doc 0110-0001 slice-10 block + fix_plan loop #33 entry.
Gates run: go build ./... OK; gofmt clean; go vet catalog OK; catalog + initdb
unit suites PASS; TestPort_PgDumpConnectionSetup PASS (getOpclasses completes).
tpch-spotcheck N/A (additive empty virtual view; zero query-path/row-count risk).

=== NEXT STEP — DU-002 slice 11 (pg_opfamily virtual view) ===
TestPort_PgDumpConnectionSetup now fails in getOpfamilies with
`relation "pg_opfamily" does not exist`. Query:
  SELECT tableoid, oid, opfmethod, opfname, opfnamespace, opfowner FROM pg_opfamily
Slice 11 = add an empty pg_opfamily virtual view (OID 2753) beside pg_opclass in
catalog.go. Schema (pg_opfamily.h): oid, opfmethod oid, opfname name,
opfnamespace oid, opfowner oid. EMPTY correct: built-in operator families are in
pg_catalog and filtered out by namespace dumpability; only user opfamilies dumped
(none here). Then continue getter battery (getAggregates, getTypes tail,
getIndexes, getConstraints…).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
