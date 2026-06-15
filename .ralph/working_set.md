Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 9 (pg_operator
virtual view) COMPLETE this loop. NOTHING in flight; next loop starts on slice 10
(pg_opclass view).

=== DONE (loop #32) — DU-002 slice 9 ===
pg_dump's getOperators runs `SELECT tableoid, oid, oprname, oprnamespace,
oprowner, oprkind, oprleft, oprright, oprcode::oid AS oprcode FROM pg_operator`;
aborted with `relation "pg_operator" does not exist`. Fix:
- internal/catalog/catalog.go (beside pg_language): added empty pg_operator
  virtual view OID 2617, schema from pg_operator.h: oid, oprname name,
  oprnamespace oid, oprowner oid, oprkind char, oprcanmerge bool, oprcanhash bool,
  oprleft oid, oprright oid, oprresult oid, oprcom oid, oprnegate oid, oprcode oid,
  oprrest oid, oprjoin oid. VirtualRows returns nil. EMPTY is correct: getOperators
  reads all operators, filters system-defined at dump-out by namespace dumpability;
  built-ins live in pg_catalog (never dumped), goopg has no user operators. oprcode
  is regproc in PG (oid-compatible) → typed oid so `oprcode::oid` resolves no-op.
- pgdump_connsetup_test.go: updated slice-8→9 next-blocker comment to pg_opclass.
- design doc 0110-0001 slice-9 block + fix_plan loop #32 entry.
Gates run: go build ./... OK; gofmt clean; go vet catalog OK; catalog + initdb
unit suites PASS; TestPort_PgDumpConnectionSetup PASS (getOperators completes).
tpch-spotcheck N/A (additive empty virtual view; zero query-path/row-count risk).

=== NEXT STEP — DU-002 slice 10 (pg_opclass virtual view) ===
TestPort_PgDumpConnectionSetup now fails in getOpclasses with
`relation "pg_opclass" does not exist`. Query:
  SELECT tableoid, oid, opcmethod, opcname, opcnamespace, opcowner FROM pg_opclass
Slice 10 = add an empty pg_opclass virtual view (OID 2616) beside pg_operator in
catalog.go. Schema (pg_opclass.h): oid, opcmethod oid, opcname name, opcnamespace
oid, opcowner oid, opcfamily oid, opcintype oid, opcdefault bool, opckeytype oid.
(Query only needs opcmethod, opcname, opcnamespace, opcowner.) EMPTY correct:
built-in operator classes are in pg_catalog and filtered out by namespace
dumpability; only user opclasses dumped (none here). Then continue getter battery
(getOpfamilies, getAggregates, getTypes tail, getIndexes…).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
