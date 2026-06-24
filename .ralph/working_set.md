Loop #18: M0118-0008 — `reindex-concurrently-toast` is the LAST unpromoted spec
(other 24 all pass strict). Landed a safe on-path correctness fix + the blocker
design doc (0118-0083); the spec itself stays `defer` (multi-loop TOAST epic).

## What landed
`InvalidOid (0)::regclass` now renders `-` (PG `regclassout`, regproc.c) instead
of matching the first virtual relation whose OID is unset
(`information_schema.routines`). Surfaced while probing the spec setup
(`reltoastrelid::regclass::text` on a no-TOAST table gave "routines"). Fix:
internal/executor/expr.go CastExpr regclass arm — `if v.Int == 0 { return "-" }`
before LookupTableByOID. Non-zero unmatched OID still falls through to numeric
rendering (matches PG). Test: internal/executor/regclass_invalid_oid_test.go
(TestRegclassInvalidOidRendersDash).

Files: internal/executor/expr.go; internal/executor/regclass_invalid_oid_test.go;
docs/design/0118-0083-reindex-concurrently-toast-blocker.md + README; deferral
ledger. (internal/testport/zz_probe_test.go = throwaway probe, untracked.)

## Why the spec is deferred (multi-loop epic — see 0118-0083)
Needs auto-TOAST-relation EXPOSURE in the catalog: PG auto-creates a TOAST
relation for ANY toastable column; goopg only emits the synthetic `pg_toast`
pg_class row when explicit `toast.*` reloptions exist. goopg ALREADY has the
out-of-line storage (toast.go, RelOid+100_000_000) and OID convention matches.
Remaining: (1) auto-set reltoastrelid + emit pg_toast row for toastable cols;
(2) resolve toast OID→name in LookupTableByOID (synthetic row is only in the
virtual pg_class builder, not c.tables); (3) synthesize toast index in pg_index;
(4) ALTER RENAME + pg_toast.<name> resolution under allow_system_table_mods;
(5) REINDEX {TABLE,INDEX} CONCURRENTLY pg_toast.<name> (rides 0118-0029).
HIGH BLAST RADIUS: relation enumeration feeds pg_dump/pg_amcheck/\d parity
suites (pgdump_connsetup, pgamcheck00{2,3}*, scripts_port) — land incrementally,
re-run each parity suite. NOT Effort-S.

## Next step
Start the TOAST-exposure epic slice 1: a catalog helper
`tableNeedsToastRelation(t)` (mirrors PG needs_toast_table — any toastable col)
+ auto-set reltoastrelid and emit the pg_toast pg_class row, THEN immediately
run pgdump_connsetup + a pgamcheck port test to measure the parity blast radius
before going further. If a parity suite breaks, gate/narrow before adding the
index + RENAME + REINDEX routing.

## Gates run (this loop)
go test ./internal/{executor,catalog}/ PASS; TestRegclassInvalidOidRendersDash +
TestPlpgSQLSelectInto PASS; go vet ./internal/executor/ clean; gofmt clean on
touched files; make ralph-state-guard (below); pgbench smoke = pre-commit hook.
