(idle — nothing in flight)

Completed this loop: **M0119-0006 69th slice** — `RegOut` schema-qualifies and
quotes the object names the reg*out family emits (ledger row 1304 resolved).
`executor.RegOut` — the single OID→name renderer shared by SELECT
(`appendTypedCellText`) and TEXT/CSV COPY (`datumToCopyText`) since the 68th
slice — now runs the regclass/regproc/regrole/regcollation arms through the new
shared `regOutQualified`: a name whose schema is NOT on the session's effective
search_path renders `schema.name` via `quoteQualifiedIdentifier` (the
ruleutils.c `quote_qualified_identifier` port, `expr.go`), with every
identifier passed through `pgQuoteIdent`'s
`internal/sqlkeywords.IsReservedForQuoting` guard. Qualification rules measured
against PG 18.3: pg_catalog objects NEVER qualify (implicitly searched — 1259
stays `pg_class` even at qualify=true), a builtin proc never qualifies, a user
table in public at qualify=true renders `public.mytable` with the `public`
qualifier itself UNQUOTED (unreserved keyword) and a `"My Table"` name quoted
(`public."My Table"`), regrole quote_identifiers the role name but a DANGLING
role OID falls to the unquoted `%u` (new `InMemory.RoleNameAtOID` in
`internal/catalog` distinguishes real roles from dangling ones — `RoleNameForOID`
renders both numerically), and regcollation quote_identifiers every name
(`C` → `"C"`, `default` → `"default"`) with user collations qualified. The
qualify flag is unchanged: COPY computes `!regObjectSchemaVisible(ctx, "public")`,
SELECT `!publicSchemaVisible(getSetting)` — and the strengthened
`TestRegCopyAndSelectSiblingQualifyAgree` now exercises a REAL user table
(created through the planner→Build pipeline), which the pre-69th 1259-only
version could not catch diverging with. New tests:
`internal/executor/reg_qualify_test.go` (qualify=true schema-qualification for
table/collation/user-routine, pg_catalog-never-qualifies, identifier quoting
incl. `"My Table"`/`public."My Coll"`/keyword role `"select"`, dangling-role
numeric) + pins in `reg_copy_test.go` + the sibling tests. Gates: package
suites PASS; pre-commit units PASS; `TestPort_RegressSuite` PASS (248 s);
`scripts/tpch-spotcheck.sh` PASS. Design
`docs/design/0119-0006-regout-schema-qualification.md` + README row (`0119-0006au`)
+ ledger row 1304 resolved + 3 new ledger rows filed + fix_plan 69th-slice entry.

**Carry-forward for a later loop (three new deferral rows under 2026-08-14, 69th
slice):** (1) `RegOut`'s regprocedure arm still returns the BARE signature
(`int4out(integer)` via `catalog.RegprocedureName`) where upstream
`regprocedureout` → `format_procedure` schema-qualifies an off-path routine name
(`public.my_udf()`), qualifying only the NAME — the `qualify` flag is already
plumbed into the arm's caller; (2) the regcollation qualify path hardcodes
`"public"` as the user-collation qualifier where upstream qualifies with the
collation's ACTUAL schema — `InMemory.FindCollation` already returns the
namespace, so the fix is to use `uc.Schema` instead of the literal and pin with
a non-public `CREATE COLLATION`; (3) goopg's role store folds every role name to
lowercase on registration, so `regroleout` can never receive a case-preserved
role name to quote (`CREATE ROLE "Alice"` renders `alice`, PG renders `"Alice"`)
— the quoting code is correct, the limitation is the catalog's missing
case-preserving display-name field (like `roleACLDisplay` but set at
role-creation time). See the ledger rows for the design docs and gate
requirements.
