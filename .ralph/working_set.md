(idle — nothing in flight)

Completed this loop: **M0119-0006 73rd slice** — the regprocedure **arglist** now
schema-qualifies non-visible arg types exactly like upstream `format_type_be`
(format_type.c:314), closing ledger row 1342 (the 71st slice's carry-forward).
Before, `formatProcedureArglist` rendered every input arg type through
`pgArgTypeDisplayAlias` only (`integer`, `text`), so a routine whose signature
references an off-path USER arg type rendered `f_offarg(mytype)` where PG 18.3
emits `f_offarg(offpath.mytype)` (an on-path/builtin arg correctly stays bare).
The fix has a capture half and a render half. **Capture:** new
`Routine.ArgTypeSchemas []string` (parallel to `ArgTypes`,
internal/catalog/routines.go) records each arg type's EXPLICIT schema at CREATE
FUNCTION/PROCEDURE (operators_ddl.go) via `argTypeSchema` returning the parser's
`ColumnType.Schema` VERBATIM (unquoted names already case-folded, quoted names
case-preserved); bare names stay `""`. It rides the existing proargdefaults JSON
round-trip (`pgProcArgMetaJSON`/`DecodePGProcArgMeta`) automatically, so pre-73rd
data dirs reload with nil → `""` → bare (backward compatible). **Render:**
`catalog.RegprocedureNameParts` now returns `[]RegprocArg{Name, Schema}` per arg
(builtin path stamps `pg_catalog`; user path reads `ArgTypeSchemas[i]`
nil-defensively); the executor-side `regprocedureArglist` (reg_identifier.go)
aliases builtin/pg_catalog args through the exported `catalog.ArgTypeDisplayAlias`
(renamed from `pgArgTypeDisplayAlias`), schema-qualifies a NON-visible user arg
via `quoteQualifiedIdentifier` with the `[]` array suffix split/re-appended
(`offpath."mytype[]"` never happens; a user-path builtin array aliases
`integer[]`), and leaves a BARE-name user arg bare (its owner schema is
unresolvable — deferral below). The session visibility predicate threads as a
variadic `visible ...func(schema string) bool` value through `RegOutArgVisible` /
`appendTypedCellText` (SELECT simple-query) / `EncodeCopyTextRow` /
`EncodeCopyCsvRow` (COPY TO) and the `::regprocedure` cast sibling in expr.go —
all four paths agree (Hard-won Rule #2). Measured against a fresh goopg cluster
+ PG 18.3 oracle (throwaway ports): `'f_offarg(offpath.mytype)'::regprocedure` →
`f_offarg(offpath.mytype)` (was `f_offarg(mytype)`), array `f_offarr(offpath.mytype[])`,
rowtype arg `f_offrow(offpath.ct)`, `f_onarg(onpath.mytype)` (name + off-path arg
both qualify), builtin `f_builtin(integer)` stays bare, a user type NAMED `int`
quotes like PG (`offpath."int"`), and the `::regprocedure` cast path additionally
qualifies the NAME (`offpath.f_offboth(offpath.mytype)`) where the wire path's
name stays bare via the documented 69th-slice proxy. One measured limitation
filed as a deferral: `SET search_path = public, offpath` cannot make the
`offpath.mytype` arg render bare yet — `searchPathSchemas` proves schema
existence only via `LookupTable(parser.ObjectName{Name: s})`, which never sees an
EMPTY schema (pre-existing proxy, surfaced on the arglist arm by this slice).
Tests: `TestRegOutRegprocedureQualifiesArgTypes` (reg_qualify_test.go),
`TestRegprocedureCastArgTypesQualify` + `TestCreateFunctionCapturesArgTypeSchemas`
(regoperator_schema_qualify_test.go), sibling
`TestRegCopyAndSelectSiblingArgQualifyAgree` (reg_copy_sibling_test.go). Gates:
package suites (internal/catalog 0.068 s, internal/executor 6.143 s,
internal/server 55.165 s) PASS; pre-commit units PASS; `TestPort_RegressSuite`
PASS (239.7 s); `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35). Design
`docs/design/0119-0006-regprocedure-argtype-schema-qualify.md` + README row
(`0119-0006ay`) + ledger row 1342 resolved + 6 new deferral rows (1343-1348) +
fix_plan 73rd-slice entry.

**Carry-forward for a later loop (seven remaining reg* deferrals under
2026-08-14, 69th-73rd slices):** (1) goopg's role store folds every role name to
lowercase on registration, so `regroleout` can never receive a case-preserved
role name to quote (`CREATE ROLE "Alice"` renders `alice`, PG renders `"Alice"`)
— the quoting code is correct, the limitation is the catalog's missing
case-preserving display-name field (ledger row 1340); (2) a BARE user-defined
arg type cannot be schema-resolved — the user-type store (enum/composite/domain/
range) has no namespace field, so `CREATE FUNCTION g(mytype)` for an off-public
type renders bare where PG qualifies it (row 1343); (3) quoted user type NAMES
lose case at CREATE (`routineArgTypeName` lowercases → `offpath."MyType"` renders
`offpath.mytype`, PG emits `offpath."MyType"` — same family as row 1340) (row
1344); (4) the catalog's bare `formatProcedureArglist` does not alias builtin
ARRAYS (`int[]` vs `integer[]`) (row 1345); (5) `ArgTypeDisplayAlias` is not a
faithful `format_type_be` port (no `varbit → bit varying`, no keyword-quoting
path for `char`) (row 1346); (6) the empty-schema visibility proxy blocks
`SET search_path = …, offpath` from rendering an `offpath` arg bare (row 1347);
(7) the pre-existing regproc/regprocedure INPUT DB-scoping bug
(`regIdentifierInput` passes no dbOid to `LookupByName`, so a live-created
routine does not resolve by name in some contexts — confirmed live during this
slice's validation: `'g_offarg'::regprocedure` failed on a stale cluster while
the reloaded `'f_offarg'` resolved) (row 1348). See the ledger rows for the
design docs and gate requirements.
