(idle — nothing in flight)

Completed this loop: **M0119-0006 70th slice** — `RegOut`'s regcollation arm
qualifies a user collation with its ACTUAL schema, not the hardcoded `"public"`
(ledger row 1339 resolved). The arm now iterates `ListUserCollations`
UNCONDITIONALLY and, on an OID match, returns
`regOutQualified(im.SchemaNameForOID(uc.NamespaceOID), n, qualify)` —
`SchemaNameForOID` is the `get_namespace_name(collnamespace)` port, and
`regOutQualified` applies the family's shared rule that also closes the
pg_catalog edge the literal could not express (a collation created in
pg_catalog is always visible → qualify=false → bare quoted name, where the old
code emitted `public.<name>`). qualify=false reaches the same bare name through
`regOutQualified`'s `!qualify` arm, so behavior is unchanged there. The qualify
flag semantics are untouched (COPY `!regObjectSchemaVisible(ctx,"public")`,
SELECT `!publicSchemaVisible(getSetting)`) — the per-object proxy imprecision is
out of scope, same as every 69th-slice family member. Measured against a
throwaway PG 18.3 oracle (port 5599): `search_path=''` renders `ragout70.mycoll`
/ `ragout70."My Other Coll"`, `search_path=ragout70` renders bare `mycoll` — the
unit pins match all three. Tests: new `TestRegCollationQualifiesWithActualSchema`
(`internal/executor/reg_qualify_test.go`: non-public plain + quoted-name
collations, qualify=false bare, public still `public.mycoll`) + sibling
`TestRegCopyAndSelectSiblingQualifyAgree` extended with the non-public collation
(both renderers must emit `other_schema.oc`). Gates: package suites PASS;
pre-commit units PASS; `TestPort_RegressSuite` PASS (239.7 s);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); pgbench pre-commit smoke PASS
(TPC-B 339, simple-update 628, select-only 12485 tps) on commit `ebfb5601`.
Design `docs/design/0119-0006-regcollation-actual-schema-qualifier.md` + README
row (`0119-0006av`) + ledger row 1339 resolved + fix_plan 70th-slice entry.

**Carry-forward for a later loop (two remaining reg* deferrals under 2026-08-14,
69th/70th slices):** (1) `RegOut`'s regprocedure arm still returns the BARE
signature (`int4out(integer)` via `catalog.RegprocedureName`) where upstream
`regprocedureout` → `format_procedure` schema-qualifies an off-path routine name
(`public.my_udf()`), qualifying only the NAME — the `qualify` flag is already
plumbed into the arm's caller (ledger row 1338); (2) goopg's role store folds
every role name to lowercase on registration, so `regroleout` can never receive
a case-preserved role name to quote (`CREATE ROLE "Alice"` renders `alice`, PG
renders `"Alice"`) — the quoting code is correct, the limitation is the
catalog's missing case-preserving display-name field (like `roleACLDisplay` but
set at role-creation time) (ledger row 1340). See the ledger rows for the design
docs and gate requirements.
