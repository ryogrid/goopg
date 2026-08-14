(idle — nothing in flight)

Completed this loop: **M0119-0006 72nd slice** — the reg* name→OID **input**
half now honors double-quoted identifiers exactly like upstream
`stringToQualifiedNameList` → `SplitIdentifierString` (varlena.c:3581), closing
ledger row 1341. Before, every reg* input arm ran the whole candidate through
`strings.ToLower` + a dumb first-dot `splitQualifiedTable`, so `'"MyFunc"'::regproc`
reached `LookupByName` with literal quotes → 42883 (PG 18.3 resolves it), and a
quoted segment's case was mangled / `.` inside quotes was split / a quoted role
with `.` was false-flagged as qualified. New shared parser in reg_identifier.go:
`splitRegIdentifiers` (full SplitIdentifierString port: quoted segment keeps case
with `""`→`"` collapse, unquoted segment downcased, whitespace skipped, syntax
error → 42602) and `splitRegQualifiedName` (first segment = schema, rest joined
with `.`). Every arm of `regIdentifierInput` (regclass/regtype/regproc/
regprocedure/regrole/regcollation) feeds through it, AND the expr.go sibling
sites — `::regproc`/`::regprocedure` cast, `::regclass` cast, `regclass()`
function-call, `pg_get_functiondef` name fallback — the input counterpart of the
69th/70th/71st slices' quote-EMISSION (sibling renderers must agree). Two
faithful addenda: `regprocedureNamePart` strips the `(…)` arg list first
(parseNameAndArgTypes' leading scan) so `'"MyFunc"(integer)'::regprocedure` does
not regress to 42602; and the parser's downcasting now makes `'C'::regcollation`
FAIL with 42704 exactly like PG 18.3 (only `'"C"'` resolves to 950 — the
collation store is the one case-sensitive name store), updating the old divergent
test. Miss messages match PG: regclass/regrole/regcollation/regtype print the
STRIPPED parsed name, regproc/regprocedure keep the RAW input (incl. parens for
regprocedure). Measured against a throwaway PG 18.3 oracle (port 5599): quoted
mixed-case routine, dotted quoted schema (`"my.schema".fn`), quote-quote collapse
(`"Weird""Quote"`), whitespace tolerance, and the 42602 cases all resolve/error
identically. Tests: new `internal/executor/reg_input_quoted_test.go` (quoted
mixed-case routine on both cast paths, dotted quoted schema, quote-quote
collapse, family siblings, 42602 on both paths, coercion route,
stripped-vs-raw miss messages); updated `reg_identifier_test.go` collation test to
the PG-faithful shape. Gates: package suites PASS; pre-commit units PASS;
`TestPort_RegressSuite` PASS (237.3 s on the confirming run — the FIRST attempt
FAILed at its 600 s timeout when the known intermittent "suite wedge"
(regress_wedge_probe_test.go) hit the `returning` case: CREATE FUNCTION parked on
the buffer-pool RWMutex while the WAL checkpointer waited on the same pool's RLock,
so no checkpoint ran, WAL grew to 7.4 GB, and crash recovery could not finish
inside the 20 s restart timeout — a storage/WAL-path cascade disjoint from this
slice; bundle preserved at tmp/regress-wedge/returning/, clean re-run PASS);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35). Design
`docs/design/0119-0006-reg-input-quoted-identifiers.md` + README row
(`0119-0006ax`) + ledger row 1341 resolved + fix_plan 72nd-slice entry.

**Carry-forward for a later loop (two remaining reg* deferrals under 2026-08-14,
69th/71st/72nd slices):** (1) goopg's role store folds every role name to
lowercase on registration, so `regroleout` can never receive a case-preserved
role name to quote (`CREATE ROLE "Alice"` renders `alice`, PG renders `"Alice"`)
— the quoting code is correct, the limitation is the catalog's missing
case-preserving display-name field (like `roleACLDisplay` but set at
role-creation time) (ledger row 1340); (2) the regprocedure arglist's
`format_type_be` does not schema-qualify non-visible arg types (an off-path
arg-type namespace renders `public.foo(integer)` where PG emits
`public.foo(public.mytype)`), invisible until a user-defined arg type + off-path
namespace is measured. Both are bounded/measure-first items; the input-direction
work this slice did NOT add the missing `::regrole`/`::regcollation` CAST arms in
expr.go (those two name→OID inputs resolve only through the `regIdentifierInput`
coercion path — pre-existing, scope-excluded in the design doc). See the ledger
rows for the design docs and gate requirements.
