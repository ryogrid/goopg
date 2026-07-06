(idle — nothing in flight)

Loop #16 completed and committed: closed the `pg_typeof(...)::oid` cast gap
named in the 2026-07-05 M0122-0005 OID-18-disambiguation row's deferred item
(2) — affected every type, not just `"char"`. Root cause: real PG declares
`pg_typeof()`'s SQL return type as `regtype`, whose C-level representation
IS an `Oid` (`pg_typeof(x)::oid` is a binary-coercible relabeling cast, not
a text parse), but goopg's `"pg_typeof"` case (`internal/executor/expr.go`)
returned a `KindString` holding display text (e.g. "integer"), so `::oid`
fell through to the generic `"oid"` cast branch and failed to
`strconv.ParseInt` it. Fix: `pg_typeof()` now evaluates to a `KindInt`
Datum holding the argument's real OID, mirroring the pre-existing
`regclass`/`regproc` representation (display text only rendered at
wire-output time) — the existing generic `"oid"` cast's `KindInt` branch
then handles `::oid` as a plain identity pass-through, no changes needed
there. New `pgTypeofOIDForName`/`RegtypeName` helpers
(`internal/executor/expr.go`) resolve name<->OID (quoted `"char"`=18,
`"unknown"`=705/`UNKNOWNOID` verified via
`postgres/src/include/catalog/pg_type_d.h`, builtins via
`catalog.TypeNameToOID`/`oidToBuiltinTypeName`, user enum/domain/
composite/range/multirange via the pre-existing `userTypeOIDForName`/
`userTypeNameForOID`). `planner.go`'s `exprType`'s `*FuncCall` case gained
a `"pg_typeof"` -> `regtype` branch; `dispatch.go` gained matching
`"regtype"` cases in `typeOIDFor`/`appendTypedCellText`. Verified live
against a real running PostgreSQL 18.3 instance side-by-side (ports 5545
goopg / 5546 real PG 18.3, both torn down after the session) — all builtin
scalar types, NULL/unknown, char-OID-18, and the M0097-0035
`pg_typeof(count(*))::oid` aggregate-fold path resolve correctly; plain
uncast `SELECT pg_typeof(...)` output is byte-identical to before; `\gdesc`
now correctly reports `regtype`. New tests:
`internal/executor/pg_typeof_oid_test.go`,
`internal/planner/pg_typeof_test.go`'s `TestExprTypePgTypeofIsRegtype`,
`internal/server/regtype_output_test.go`. Verified: `go build ./...` clean;
`go test ./internal/executor/... ./internal/planner/... ./internal/server/...
./internal/catalog/... ./internal/parser/...` all PASS; tpch-spotcheck PASS
(Q12=2/Q13=33); `make ralph-state-guard` OK (auto-repaired a stale
status/progress marker, same recurring benign class as prior loops). Design
doc (`docs/design/0122-0005-char-oid18-disambiguation.md`) new "Follow-up:
pg_typeof(...)::oid cast" section + README row extended. Ledger row
appended (status `-`): newly discovered, pre-existing (NOT introduced by
this fix — reproduced via the untouched pre-existing `'name'::regtype`
cast), is `userTypeNameForOID` unconditionally prefixing user-defined type
names with "public." regardless of `search_path` visibility, unlike the
more careful `regObjectSchemaVisible` check `regproc`/`regoperator` already
use.

Next candidate (pick ONE): the `userTypeNameForOID` schema-visibility gap
just recorded (bounded — extend `regObjectSchemaVisible`-style check to a
3rd `reg*` direction, ~3 call sites), the view's-own-ACL gap from M0122-0008
(materially larger — needs a preliminary per-statement RTE-style permission
pass, planning currently has no session-role visibility), resume the
M0110-0001 multi-database isolation survey (fix_plan "Current Priority"
banner — per-database catalog/storage isolation, milestone-scale,
repeatedly deferred across many loops as too large for one loop), or survey
`.ralph/deferral_ledger.md` for another fresh open (`status = -`) row.
