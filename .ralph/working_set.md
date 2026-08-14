(idle — nothing in flight)

Completed this loop: **M0119-0006 75th slice** — the regproc/regprocedure
NAME→OID **input** half is now scoped to the connection's database, closing
deferral row 1348 (the 73rd slice's carry-forward). The 4e-series routine
registry (M0122-0007 slice 4e) keys routines by `(dbOid, schema, name)`, but
`regIdentifierInput` and the expr.go `::regproc`/`::regprocedure` cast sibling
called `Routines.LookupByName` with NO dbOid, so they always resolved
`DefaultDBOid`: a LIVE-created routine (registered under its real dbOid) was
invisible by name from a distinct-dbOid connection — and worse, a same-named
routine in ANOTHER database resolved THAT routine's OID (a silent cross-dbOid
leak: `'shared_fn'::regproc` from db2 returned DefaultDBOid's 131072 instead of
its own 131073). An initdb-reloaded routine (DefaultDBOid) still resolved,
which hid the bug on default-database connections. **Fix:** both sibling paths
(Hard-won Rule #2) thread `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` into
`LookupByName` — `regIdentifierInput`'s regproc/regprocedure arm
(internal/executor/reg_identifier.go, feeds COPY FROM coercion + constraint
checks) and expr.go's `::regproc`/`::regprocedure` cast arm (feeds
`'name'::regproc` in expressions) — mirroring the regclass arm's existing
connDBOid. Builtins still resolve via the global `LookupBuiltinProc` pg_proc
index (pg_catalog implicitly visible in every database, matching PG). Tests:
`TestRegProcInputScopedToConnectionDBOid`,
`TestRegProcInputSchemaQualifiedScopedToConnectionDBOid`,
`TestRegProcInputDistinctDBOidMissIsNotDefaultLeak`
(internal/executor/reg_identifier_dbid_scoping_test.go) — all FAIL pre-fix (the
first two leaked the wrong OID, the third resolved instead of raising 42883) and
PASS post-fix. Live E2E on a throwaway goopg (5533) + byte-identical PG 18.3
oracle (5534): db2's `'shared_fn'::regproc` → 42883 before its own routine
exists, then each database resolves its own OID (131072 vs 131073). Gates:
package suites (internal/executor 5.965s) PASS; pre-commit units PASS (initdb
426.485s re-ran cold); `TestPort_RegressSuite` PASS (236.314s);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35, query phase 23.3s). Design
`docs/design/0119-0006-regproc-input-dbid-scoping.md` + README row `0119-0006ba`
+ ledger row 1348 resolved + fix_plan 75th-slice entry.

**Carry-forward for a later loop (six remaining reg* deferrals under
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
(5) **NEW** — MULTI-WORD type names in CREATE FUNCTION args store the LAST word:
the parser's collapsed `ColumnType.Name` is used verbatim, so `bit varying` →
`varying`, `character varying` → `varying`, `double precision` → `precision`,
and `timestamp with time zone` is a syntax error in CREATE FUNCTION args;
measured live: `CREATE FUNCTION f_vchar(bit varying)` renders `f_vchar(varying)`
(PG: `f_vchar(bit varying)`). Separately the arglist carries only `Name`
(dropping Args/OID), so a BARE `char` arg — parser-stamped bpchar-like,
`Args=[1]`, like PG — is indistinguishable from OID-18 `"char"` and renders
`"char"` where PG renders `character` (row 1349); (6) **NEW** — a reg* →
text/varchar/name cast on a STRING-LITERAL source renders the raw OID:
`'f_varbit(varbit)'::regprocedure::text` → `131072` (PG: `f_varbit(bit
varying)`), `'f_varbit'::regproc::text` → `131072`, `'pg_type'::regclass::text`
→ `1247`; the `::regprocedure` INPUT half resolves correctly, only the
downstream cast to text/name/varchar renders the numeric datum.
regtype/regrole/regcollation and non-literal sources unaffected (row 1350). See
the ledger rows for the design docs and gate requirements.
