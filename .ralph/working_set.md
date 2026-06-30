Loop #34 COMPLETE: M0119-0004 DU-002 slice 394 — a table column / composite
field declared `COLLATE <user-collation>` now round-trips through real pg_dump
18.3 as the schema-qualified `COLLATE public.usercoll` (vs the built-in
`pg_catalog."C"` form). Closes the composition gap left by slices 389–393
(user collation dumps) + slice 188 (built-in-collation column dumps).

Root cause: the attcollation surfacing resolved the declared collation name to an
OID only via `collationNameToOID` (the 7 built-ins), so a user-collation name fell
through to the type default (typcollation); pg_dump's getTableAttrs saw no
`attcollation <> typcollation` difference and emitted no COLLATE clause.

Files (committed):
- internal/catalog/catalog.go: new `UserCollationOIDByName` (after CollationAttrsByName).
- internal/executor/pg18_user_catalog_rows.go: fallback to UserCollationOIDByName at
  BOTH attcollation sites (buildUserPGAttributeRow ~786, ...ForCompositeField ~1706)
  when collationNameToOID returns 0.
- internal/testport/pgdump_connsetup_test.go: usercoll/usercollcol fixture (~4983) +
  block-scoped assertion (~7711).
- docs/design/0110-0001-pg-dump-tap-port.md: Slice 394 section.
- .ralph/fix_plan.md slice 394 entry; deferral ledger row appended.

Gates: TestPort_PgDumpConnectionSetup PASS (5.9s, byte-identical vs real pg_dump
18.3); go build ./... clean; catalog + executor/parser collation units PASS.
pgbench smoke runs via pre-commit hook on commit.

Next loop: fresh M0119-0004 pg_dump slice. Collation dump fidelity is now complete
(CREATE COLLATION all providers, COMMENT, column/composite/index COLLATE for both
built-in and user collations). Candidates: per-schema collation disambiguation +
heap-backed pg_collation persistence (recurring 389–394 deferral, larger); a new
object family — CREATE CONVERSION (needs conproc regproc + pg_encoding_to_char +
real executor support), CREATE CAST WITHOUT FUNCTION (pg_cast catalog), or
aggregates (prokind='a' + pg_aggregate).
