(idle — nothing in flight)

Completed this loop: **M0119-0006 74th slice** — the regprocedure arglist's
`ArgTypeDisplayAlias` became a faithful `format_type_be` port AND the catalog
bare arglist builder aliases builtin arrays, closing ledger rows 1345 + 1346
(the 73rd slice's carry-forwards). Two divergences from PG 18.3: (a) the shared
alias table had no `varbit → bit varying` arm (the one missing VARBITOID
special-case switch entry — `bit`/`interval`/`json`/`numeric` are identities,
every other case was already present) and no keyword-quoting path at all, so
the single-byte `char` (CHAROID, distinct from bpchar/1042) rendered bare
`char` where `format_type_be`'s DEFAULT path runs
`quote_qualified_identifier` and `quote_identifier` wraps the lexer keyword →
`"char"`; (b) the catalog BARE builder `formatProcedureArglist` (behind
`RegprocedureName`/`RegprocedureNameAndSchema`) passed the WHOLE stored name —
baked-in `[]` array suffix included — to the alias, so `int[]` found no switch
case and rendered `f(int[])` where the executor's pg-faithful
`regprocedureArglist` already emitted `f(integer[])`: the two sibling renderers
diverged on a builtin array arg (Hard-won Rule #2). **Fix:** two new arms in
`catalog.ArgTypeDisplayAlias` (`varbit → bit varying`, `char → "char"`,
internal/catalog/catalog.go) and a package-local `argListTypeDisplay` helper
(catalog cannot import executor) that splits a `[]` suffix, aliases the ELEMENT,
re-appends — mirroring executor's `splitArraySuffix` (reg_identifier.go). The
executor renderer needed NO change; it picks up the new arms via the shared
alias. Measured vs a throwaway PG 18.3 oracle (port 5533) on the wire path
(`oid::regprocedure`): `f_varbit(bit varying)`, `f_char("char")`,
`f_chararr("char"[])`, `f_intarr(integer[])` now byte-identical; the two
siblings agree on `integer[],bit varying,"char","char"[],double
precision[],text` (pinned by `TestRegprocedureArglistCatalogAndExecutorAgree`).
Tests: `TestArgTypeDisplayAliasFormatTypeBePort` +
`TestRegprocedureName` array/varbit/char cases (internal/catalog/
regproc_name_test.go), `TestRegOutRegprocedureArgTypesVarbitChar` +
`TestRegprocedureArglistCatalogAndExecutorAgree` (internal/executor/
reg_qualify_test.go). Gates: package suites (internal/catalog 0.063s,
internal/executor 6.103s, internal/server 55.151s) PASS; pre-commit units PASS;
`TestPort_RegressSuite` PASS (237.459s — a first CONCURRENT run hit the Go
default 600s `-test.timeout` while the pre-commit units were hammering the
machine, leaking a spinning goopg orphan at ~25GB RSS which was SIGKILLed; the
re-run alone passed, matching the 73rd slice's 239.7s); `scripts/tpch-spotcheck.sh`
PASS (Q12=2, Q13=35, query phase 21.4s). Design
`docs/design/0119-0006-argtype-alias-format-type-be-port.md` + README row
`0119-0006az` + ledger rows 1345/1346 resolved + 2 NEW deferral rows (1349,
1350) + fix_plan 74th-slice entry.

**Carry-forward for a later loop (nine remaining reg* deferrals under
2026-08-14, 69th-74th slices):** (1) goopg's role store folds every role name to
lowercase on registration, so `regroleout` can never receive a case-preserved
role name to quote (`CREATE ROLE "Alice"` renders `alice`, PG renders `"Alice"`)
— the quoting code is correct, the limitation is the catalog's missing
case-preserving display-name field (ledger row 1340); (2) a BARE user-defined
arg type cannot be schema-resolved — the user-type store (enum/composite/domain/
range) has no namespace field, so `CREATE FUNCTION g(mytype)` for an off-public
type renders bare where PG qualifies it (row 1343); (3) quoted user type NAMES
lose case at CREATE (`routineArgTypeName` lowercases → `offpath."MyType"` renders
`offpath.mytype`, PG emits `offpath."MyType"` — same family as row 1340) (row
1344); (4) the empty-schema visibility proxy blocks
`SET search_path = …, offpath` from rendering an `offpath` arg bare (row 1347);
(5) the pre-existing regproc/regprocedure INPUT DB-scoping bug
(`regIdentifierInput` passes no dbOid to `LookupByName`, so a live-created
routine does not resolve by name in some contexts) (row 1348); (6) **NEW** —
MULTI-WORD type names in CREATE FUNCTION args store the LAST word: the parser's
collapsed `ColumnType.Name` is used verbatim, so `bit varying` → `varying`,
`character varying` → `varying`, `double precision` → `precision`, and
`timestamp with time zone` is a syntax error in CREATE FUNCTION args; measured
live: `CREATE FUNCTION f_vchar(bit varying)` renders `f_vchar(varying)` (PG:
`f_vchar(bit varying)`). Separately the arglist carries only `Name` (dropping
Args/OID), so a BARE `char` arg — parser-stamped bpchar-like, `Args=[1]`, like
PG — is indistinguishable from OID-18 `"char"` and renders `"char"` where PG
renders `character` (row 1349); (7) **NEW** — a reg* → text/varchar/name cast on
a STRING-LITERAL source renders the raw OID: `'f_varbit(varbit)'::regprocedure::text`
→ `131072` (PG: `f_varbit(bit varying)`), `'f_varbit'::regproc::text` →
`131072`, `'pg_type'::regclass::text` → `1247`; the `::regprocedure` INPUT half
resolves correctly, only the downstream cast to text/name/varchar renders the
numeric datum. regtype/regrole/regcollation and non-literal sources unaffected
(row 1350). See the ledger rows for the design docs and gate requirements.
