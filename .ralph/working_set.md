Task: M0134-0071 (equivclass.sql) — Bucket A (`LANGUAGE internal` CREATE
FUNCTION) landed and committed `f70edc85`; case PARKED (not flipped to pass).

Landed: `internal/executor/operators_ddl.go` `execCreateFunction`/`execCreateProcedure`
allowlist now admit `internal`; new `catalog.LookupBuiltinProcByProname`
(name→OID reverse of `pgProcNamesByOID`) binds `AS '<name>'` (unknown → 42883,
PG `fmgr_internal_validator` `pg_proc.c:746/770-771`); `plpgsql_runtime.go`
`dispatchStoredRoutineByLanguage` gains a real `case "internal"` →
`dispatchInternalFunction` (int8eq/ne/lt/le/gt/ge, btint8cmp, int8in/out,
hashint8; strict; Datum-level — no coercion needed for `like int8` aliases);
`hash_partition.go` `pgHashInt8` (PG `hashfunc.c:84`). Design
`docs/design/m0134-0071-language-internal-function.md` + README index; test
`internal/executor/create_function_language_internal_test.go`.

Result: equivclass **594 → 573 diff lines, 40 → 28 `^+ERROR`** (10 `language
internal` + 2 cross-type `only boolean operators` cleared). CSV row stays
`failed`/`pass_required=no`; no `make regen-testport`.

Key finding (re-attribution): the 13 `incompatible operand types` errors are an
INDEPENDENT analyzer gap, NOT a cascade of the internal-language wall — the
BinaryOp type-check (`analyzer.go:1359-1362`/`:3186`) is a builtin name-switch
with no user-operator lookup, so `int8 = int8alias1` (a user operator) is never
resolved. Largest contained next slice (16/28 remaining). Bucket C (built-in
`integer_ops` opfamily rows) is second. Both ledgered 2026-08-22.

Next step: M0134-0072 (temp.sql) — regress-sql `failed`, not yet sized. Same
pattern: researcher sizes (run `scripts/pg-regress-runner.sh --verbose temp`),
then pick the largest contained slice, design + implement.

Gates run this loop: researcher ran the regress runner twice (deterministic, 594
lines, exit 1); implementer `go test ./internal/executor/` + `./internal/catalog/`
PASS, `go build ./...` PASS, regress 594→573/40→28; pre-commit pgbench smoke
PASS (0 failed, `f70edc85`).

Delegation: researcher m0134-0071-equivclass-sizing DONE; implementer
m0134-0071-internal-lang DONE (report delivered inline — the harness blocked the
`report.md` Write). No open brief.

In-flight: none.
