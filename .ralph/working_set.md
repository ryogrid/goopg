(idle — nothing in flight)

Completed this loop: **M0119-0006 71st slice** — `RegOut`'s regprocedure arm
qualifies ONLY the routine NAME, per `format_procedure_extended` (regproc.c:326),
not the whole signature (ledger row 1338 resolved). The arm had returned the
BARE signature (`my_udf()` via `catalog.RegprocedureName`) where upstream
schema-qualifies the routine NAME when it is off the session's effective
search_path and quote_identifiers the name in BOTH arms; the `format_type_be`
arglist is appended UNQUOTED. New `catalog.RegprocedureNameParts` resolves an OID
to the `(schema, name, arglist)` halves (refactored out of
`RegprocedureNameAndSchema`; the NAME is returned raw — quoting/qualification is
the renderers' job since both arms share the parts). The RegOut regprocedure arm
routes them through the family's shared `regOutQualified(schema, name, qualify)`
and appends the unquoted arglist. The `::regprocedure` cast path (expr.go) — the
SIBLING renderer (Hard-won Rule #2) — switched from the old
`schema + "." + sig` whole-signature prefix (which also skipped quote_identifier
on mixed-case names) to the same form, fixing the on-path mixed-case case too
(`"MyFunc"(integer)` not `MyFunc(integer)`) and keeping the two renderers
byte-identical. Measured against a throwaway PG 18.3 oracle (port 5599): default
path renders `udf71(integer,text)` / `"MyFunc71"(integer)` / `ragout71.other_func()`
/ `ragout71."Quoted Other"(integer)`, `search_path=''` renders
`public.udf71(integer,text)` / `public."MyFunc71"(integer)`, `search_path=ragout71`
renders bare `other_func()` / `"Quoted Other"(integer)`, builtin `int4out(integer)`
never qualifies. Tests: new `TestRegOutRegprocedureQualifiesNameOnly`
(`internal/executor/reg_qualify_test.go`; qualify=true/false, arglist rendering,
mixed-case quoted name both arms, non-public schema, builtin) +
`TestRegprocedureCastQuotesRoutineName` (`regoperator_schema_qualify_test.go`; the
cast sibling incl. a non-public-schema routine qualifying on both paths) + sibling
`TestRegCopyAndSelectSiblingQualifyAgree` extended with a user routine
(`public.regproc_sibling_udf()` at qualify=true, both renderers). Gates: package
suites PASS; pre-commit units PASS; `TestPort_RegressSuite` PASS (242.3 s);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35). Design
`docs/design/0119-0006-regprocedure-qualified-name.md` + README row
(`0119-0006aw`) + ledger row 1338 resolved + fix_plan 71st-slice entry.

**Carry-forward for a later loop (three remaining reg* deferrals under
2026-08-14, 69th/71st slices):** (1) goopg's role store folds every role name to
lowercase on registration, so `regroleout` can never receive a case-preserved
role name to quote (`CREATE ROLE "Alice"` renders `alice`, PG renders `"Alice"`)
— the quoting code is correct, the limitation is the catalog's missing
case-preserving display-name field (like `roleACLDisplay` but set at
role-creation time) (ledger row 1340); (2) NEW — `regprocin`'s name→OID INPUT
direction still ToLower's quoted identifiers (`'"MyFunc"'::regproc` fails 42883),
so the output side renders quoted names PG-faithfully but the input side cannot
resolve them — resume at `regIdentifierInput` (expr.go ~536-556); (3) NEW — the
regprocedure arglist's `format_type_be` does not schema-qualify non-visible arg
types (an off-path arg-type namespace renders `public.foo(integer)` where PG
emits `public.foo(public.mytype)`), invisible until a user-defined arg type +
off-path namespace is measured. See the ledger rows for the design docs and gate
requirements.
