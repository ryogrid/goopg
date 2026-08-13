(idle — nothing in flight)

Completed this loop: **M0119-0006 68th slice** — TEXT/CSV COPY of a `reg*`
column renders its name↔OID (ledger row 1303 resolved). The family-wide gap
the 66th/67th slices shipped for every object-identifier member is closed:
`datumToCopyText` (shared by TEXT `EncodeCopyTextRow` and CSV
`EncodeCopyCsvRow`) gains a reg* guard routing any `regproc`/`regprocedure`/
`regclass`/`regtype`/`regrole`/`regcollation` KindInt datum through the new
exported `executor.RegOut` — the SAME OID→name renderer `appendTypedCellText`'s
six reg* cases now collapse onto (one call, no duplication — Hard-won Rule #2),
so COPY TO cannot drift from SELECT and OID 0 → "-" for every family (fixing
the pre-68th SELECT regclass case that matched an OID-0 information_schema
virtual table for a nondeterministic name). The renderers gain `(cat
catalog.Catalog, qualify bool)`, threaded from `RunCopyTo` with `qualify =
!regObjectSchemaVisible(ctx, "public")` (the server's
`!publicSchemaVisible(getSetting)`). COPY FROM routes the decoded row through
`coerceRowForConstraintChecks` at `insertSourceRow` with a reg*-only include
filter (`isRegIdentifierTypeName` — the exact 6-name family, numeric-only
`oid`/`cid` excluded so the wider encode/align lists are untouched), so a name
field resolves to its OID via the 67th slice's choke point with the family's
OWN SQLSTATE unwrapped (42P01 regclass / 42704 regrole+collation — NOT the
22P04 wrap `copyTextToDatum` would add), and `-`/pure-digit fields stay numeric
OIDs via the 66th slice's `parseDashOrOid`. New tests:
`internal/executor/reg_copy_test.go` (TO renders names across TEXT+CSV for all
six incl. pg_class 1259 / role alice / collation mycoll; OID 0 → "-"; KindString
passthrough; FROM resolves name/-/numeric; the include filter leaves a non-reg*
column untouched; the family predicate) + `internal/server/reg_copy_sibling_test.go`
(SELECT vs COPY byte-agreement at qualify false AND true). Gates: package
suites PASS; pre-commit units PASS; `TestPort_RegressSuite` PASS (340 s);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35). Design
`docs/design/0119-0006-reg-copy-text-name-rendering.md` + README row + ledger
row 1303 resolved + 4 new ledger rows filed + fix_plan 68th-slice entry.

**Carry-forward for a later loop:** `RegOut` returns the BARE object name —
upstream `regclassout`/`regprocout`/`regroleout`/`regcollationout` schema-qualify
a name NOT visible on the search_path and quote it via `quote_qualified_identifier`
(regproc.c), so a reg* column whose object lives in a non-default schema
diverges from PG in the emitted text (SELECT and COPY equally — an inherited
SELECT-path gap this slice did not widen). The `qualify` flag is already plumbed
into `RegOut`; the next slice threads it into a `quote_qualified_identifier`
port (`schema.name` when not visible, quoting via the
`internal/sqlkeywords.IsReservedForQuoting` guard the pgQuoteIdent trio shares).
See the first new ledger row under 2026-08-14 (68th slice).
