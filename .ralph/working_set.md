(idle — nothing in flight)

Loop #60 landed + committed (6c69a8ab): `UserAggregate.NamespaceOID`,
closing DU-002 slice 405 resume point (a) ("pronamespace hardcoded to
public"). CREATE AGGREGATE in a non-public schema now round-trips
schema-qualified through pg_dump (new fixture `s.schemedavg` in
`TestPort_PgDumpConnectionSetup`), survives a restart (WAL schema-name
field + recovery resolution), mirrors the `UserCollation.NamespaceOID`
pattern end to end. All gates passed (build, vet, -race wal+mvcc,
catalog/executor/parser/initdb suites, TPC-H Q12=2/Q13=33, pgbench smoke).

Next candidate (M0119-0004, from this loop's fresh ledger row): slice 405
resume point (b) — built-in aggregates' `aggtransfn`/`aggfinalfn`/... still
render raw numeric OIDs (not names) on a direct `SELECT aggtransfn::regproc
FROM pg_aggregate` query. Needs a curated reverse OID→proc-name table for
the 161 built-in aggregate support functions (same shape as
`catalog.LookupBuiltinProc`'s forward table). No current pg_dump fixture
reads aggtransfn/aggfinalfn directly (dumpAgg already renders the
SFUNC=/FINALFUNC= clause text correctly via the routine registry /
knownBuiltinAggFinalFuncs) — so this is a "direct catalog query" gap, not a
pg_dump gap; a probe fixture would need `psql -c "SELECT aggtransfn::regproc
FROM pg_aggregate WHERE ..."` style assertion rather than a connsetup dump
diff. Check `.ralph/deferral_ledger.md` tail for full resume detail.

Otherwise: M0119-0005 (pg_waldump server tier), M0119-0006 (pg_amcheck
server tier), M0119-0007 (pg_basebackup recvlogical, blocked on logical
decoding) are the other open M0119 items — see fix_plan.md.
