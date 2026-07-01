(idle — nothing in flight)

Loop #61 landed + committed + pushed (c33f7c64): built-in `pg_aggregate`
regproc columns (`aggtransfn`/`aggfinalfn`/`aggcombinefn`/`aggserialfn`/
`aggdeserialfn`/`aggmtransfn`/`aggminvtransfn`/`aggmfinalfn`) now render
`pg_proc.proname` text instead of raw numeric OIDs on the 161 BKI rows,
closing DU-002 slice 405 resume point (b). New `pgProcNameForOID` in
`internal/initdb/pg_aggregate_view.go` indexes the already-generated
`pgProcAllEntries()` (3397-row PG18 `pg_proc.dat`) by OID via a lazy
`sync.Once` map; `aggBuiltinFuncName` wraps it (0 → "-", mirrors the
existing `aggFuncNameOrDash` convention already used for user aggregates).
No planner change needed — `TypedVirtualCell` already falls a non-numeric
regproc cell through to `StringConst`. New tests in
`internal/initdb/pg_aggregate_view_test.go` (4 tests, all passing,
including a guard that ALL 161 BKI rows' non-zero regproc OIDs resolve to
real names). Design doc `0110-0001-pg-dump-tap-port.md` updated. Gates:
build/vet clean, new tests PASS, TestPort_PgDumpConnectionSetup PASS,
catalog/executor/parser/initdb suites PASS, TPC-H Q12=2/Q13=33 PASS,
pgbench smoke = pre-commit hook PASS.

Fresh ledger row this loop's deferral: goopg still has **no general
OID→name resolution for `regproc`-typed columns at query-output time** —
confirmed via two independent investigation agents that `pg_type.typinput`/
`typoutput`/..., `pg_operator.oprcode`/`oprrest`/`oprjoin`, `pg_am.amproc`
all still render raw OID numbers on a direct SELECT (no `case "regproc"` in
either `internal/server/dispatch.go`'s or `dispatch_extended.go`'s
per-column-type text-formatting switch, unlike the existing `regclass` case
in `dispatch.go` which DOES resolve OID→name). Also newly noted as a
separate pre-existing gap: `dispatch_extended.go`'s per-cell type switch is
missing several cases `dispatch.go` has (`regclass`/`date`/`time`/`bytea`)
— the extended (Bind/Execute) protocol already renders some typed columns
less faithfully than simple-query. Neither is fixed by this loop; both are
recorded in `.ralph/deferral_ledger.md` tail with concrete resume points
(promote `pgProcNameForOID`-equivalent out of `internal/initdb` since
`internal/executor`/`internal/server` cannot import it — cycle).

Next candidates: (1) the regproc-generic-output gap above (bigger, touches
both wire-protocol dispatch paths); (2) dispatch_extended.go vs dispatch.go
type-switch divergence (its own smaller item); (3) M0119-0005 (pg_waldump
server tier), M0119-0006 (pg_amcheck server tier), M0119-0007
(pg_basebackup recvlogical, blocked on logical decoding) — see fix_plan.md.
M0119-0002 (CLOG store swap Part B) also still open.
