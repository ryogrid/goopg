(idle — nothing in flight)

Last completed (this loop, 2026-07-04): closed the long-open DU-002 slice
375/380/421 deferral item — `CREATE`/`ALTER FOREIGN DATA WRAPPER ... [HANDLER
f|NO HANDLER] [VALIDATOR f|NO VALIDATOR]` now resolves the function name
against the live routine registry instead of discarding it, mirroring
`foreigncmds.c`'s `lookup_fdw_handler_func`/`lookup_fdw_validator_func`
(handler = niladic routine returning `fdw_handler`, 42809/42883 on mismatch;
validator = exactly `(text[], oid)`, return type ignored). This work was
already in progress as an uncommitted diff at the start of this loop —
verified it end-to-end, finished the documentation trail, and landed it: new
parser `scanFDWFuncClause` + `CompatNoopStmt.FDWHandlerFunc`/`FDWValidatorFunc`/
`FDWHandlerGiven`/`FDWValidatorGiven` (`internal/parser/ast.go`, `ddl.go`);
new executor `resolveFDWHandlerFunc`/`resolveFDWValidatorFunc`
(`internal/executor/operators_ddl.go`) wired into both CREATE and ALTER
`execCompatNoop` arms before the catalog write; `catalog.ForeignDataWrapper`
gains `HandlerOID`/`ValidatorOID uint32`, surfaced by
`pg_foreign_data_wrapper.VirtualRows` so the pre-existing `::regproc` cast
resolves real names instead of always `-`.

Design doc `docs/design/0119-0004-create-operator-roundtrip.md` gained a new
"Loop #76" section; `docs/design/README.md` gained index row `0119-0004da`.
Deferral ledger row appended (status `resolved`) closing the slice
375/380/421 open item — remaining scope (real FDW handler *execution*, not
just catalog bookkeeping) recorded as a separate, much larger, unforced
follow-up.

Committed (`e7fa067b`) and pushed to `align-data-structure-with-pg`.

Gates run this loop: `go build ./...` clean; `go vet` clean;
`go test -count=1 ./internal/parser/... ./internal/catalog/...
./internal/executor/...` PASS (new tests: `TestParseCreateFDWHandlerValidator`,
`TestCreateForeignDataWrapperHandlerValidatorResolved`,
`TestCreateForeignDataWrapperHandlerErrors`,
`TestAlterForeignDataWrapperHandlerValidatorSetAndClear`);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke: first
pre-commit attempt hit a pre-existing flaky 1-in-5279 "current transaction is
aborted" TPC-B abort (unrelated to this diff — pure FDW-DDL parser/catalog
change, nowhere near the pgbench write path); reran the smoke standalone
(0 failed/7334 txns) to confirm it wasn't a regression, then the actual
commit's pre-commit hook run passed cleanly (0 failed across all 4 pgbench
phases). `make ralph-state-guard` self-repaired the same recurring benign
progress.json "completed" artifact noted in prior loops' carries (expected
every loop, not a defect).

Note for a future loop (not urgent, low-frequency): the flaky pgbench abort
("current transaction is aborted, commands ignored until end of transaction
block", 1 failed txn out of 5279) seen once this loop but not reproduced on 3
subsequent clean runs is NOT root-caused. Unrelated to this loop's change;
if it recurs with any regularity it deserves investigation — likely a narrow
concurrency/serialization-error-without-retry edge in the TPC-B path
(pgbench's default script has no retry-on-serialization-failure, so any
transient 40001/40P01 surfaces exactly like this).

Next step: no work in flight. Pick the next item from
`.ralph/deferral_ledger.md` (status `-`) or `docs/design/README.md`'s open
items — the root-0025 (updatable views) design doc still lists deferred
items (1)-(4) (column subset/reorder/rename, view-of-view chaining,
`UPDATE...FROM`/`DELETE...USING` a view, CHECK OPTION on partition-routed
rows) as bounded, independently-scoped follow-ups; alternatively resume the
M0119-0004 pg_dump catalog-view parity battery via
`TestPort_PgDumpConnectionSetup` (has been the discovery engine for DU-002
slices through at least 445).
