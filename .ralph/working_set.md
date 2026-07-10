(idle — nothing in flight)

## Loop summary (2026-07-10, loop #10)

**Outcome: M0122-0022 — verified & closed the COLLATE/USING
`ALTER TYPE … ALTER ATTRIBUTE` `unimplemented_feat.json` entry
(task_id `M0110-0001`, deferred 2026-06-20). No code change to
non-test files — verify-before-implement finding, same pattern as
M0122-0019/0020/0021 (loops #7/#8/#9). Added a new regression test
(previously untested despite being wired). Committed + pushed.**

- Nightly triage: sole action item AI-20260710-011513-001 (build failure)
  already had a closed M-NIGHTLY task from loop #6; `go build ./...` clean
  at loop start — no new M-NIGHTLY task needed.
- Spawned a survey agent over 4 candidates named at the end of loop #9's
  working_set (#67 pg_get_expr stub, #80 COLLATE/USING ALTER TYPE ALTER
  ATTRIBUTE, #88 WITH OPTIONS column clause, #93 EXCLUDE USING type
  validation). Agent + my own follow-up checks found: pg_get_expr genuinely
  still a stub (confirmed open, real gap — pass-through only, no tree
  reconstruction); WITH OPTIONS genuinely still a no-op (confirmed open);
  EXCLUDE USING genuinely still bypasses type validation (confirmed open) —
  but COLLATE/USING in ALTER TYPE ALTER ATTRIBUTE was STALE.
- Verified in depth: COLLATE is fully wired — parser captures
  `AlterAttrCollation` (`internal/parser/ddl.go:9733`), BOTH executor paths
  apply it (single-subcommand `execAlterType`,
  `internal/executor/operators_ddl.go:18776`; multi-subcommand
  `execAlterTypeAttrCmds`, `operators_ddl.go:18990` — checked as sibling
  paths per `pattern_sibling_paths_must_agree`), and
  `buildUserPGAttributeRowForCompositeField`
  (`internal/executor/pg18_user_catalog_rows.go`) writes the collation into
  `pg_attribute.attcollation` for `pg_dump` round-trip. USING is not part of
  this statement's real PG grammar at all — confirmed against
  `postgres/src/backend/parser/gram.y`'s production `ALTER ATTRIBUTE ColId
  opt_set_data TYPE_P Typename opt_collate_clause opt_drop_behavior` (only
  COLLATE + CASCADE|RESTRICT; USING is exclusive to `ALTER TABLE … ALTER
  COLUMN TYPE`) — so the entry's USING claim was inapplicable from the
  start.
- Added `internal/executor/alter_type_attribute_collate_test.go`
  (`TestAlterTypeAlterAttributeCollateApplied`) — single-subcommand form,
  multi-subcommand form, and the COLLATE-reset-on-retype-without-COLLATE
  case. This was previously UNTESTED at the executor level (only parser-level
  AST tests existed for the COLLATE capture, not that it actually gets
  applied) — closing this loop with real coverage, not just an audit note.
- Flipped `unimplemented_feat.json`'s M0110-0001 entry `open`→`resolved` via
  surgical 2-line `Edit` (NOT full rewrite): 83/181 resolved, 98 open (was
  82/99).
- Added `.ralph/fix_plan.md` entry `M0122-0022`. No design-doc change
  needed (parser/DDL composite-type behavior already covered by existing
  docs).
- Gates run (foreground, all PASS): `go build ./...`, `go vet ./...`;
  `go test ./internal/executor/... -run TestAlterTypeAlterAttributeCollateApplied`;
  `go test ./internal/executor/... -run 'TestAlterType|TestComposite'`;
  `go test ./internal/executor/...` (full package, ~4s); `scripts/tpch-spotcheck.sh`
  (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh`
  (0 failed, all 3 workloads); `make ralph-state-guard` (self-repaired a stale
  running/completed mismatch, same pattern as loops #7/#8/#9).
- Committed, pathspec-scoped to `.ralph/fix_plan.md`, `.ralph/progress.json`,
  `.ralph/working_set.md`, `unimplemented_feat.json`,
  `internal/executor/alter_type_attribute_collate_test.go` — did NOT touch
  the concurrently-modified `ci/logs/*`, `analysis/*`, `postgres` submodule,
  or untracked `weekly_loc.*` files sitting in the tree from another
  process. Pushed to `origin/align-data-structure-with-pg`.

**Next natural work:** continue surveying `unimplemented_feat.json`'s
remaining 98 open entries for more stale ones (quick wins) — 3 candidates
from this loop's survey are CONFIRMED still genuinely open (real
implementation gaps, not stale audits): #67 `pg_get_expr()` (returns the
pre-formatted string pass-through only, never reconstructs from an actual
node tree — likely fine in practice since every populated pg_node_tree
column already stores pre-formatted text, but worth a closer architecture
review before declaring done), #88 `WITH OPTIONS` column clause (parser
literally no-ops the tokens; note the *unrelated* generated-column-override
feature for `PARTITION OF` already works via `ColGeneratedExprs`, so rescope
narrowly if picked up), #93 `EXCLUDE USING` type-validation bypass
(`createExclusionIndexStub`, `internal/executor/operators_ddl.go:9878`, zero
type-compatibility checks, catalog-only, doc comment admits "not enforced in
v0" — this one is a real, bounded implement candidate, not just a stub-audit
close). OR M0122-0007's remaining real scope (index/typed-table TEMPLATE
copying, per-database index/type catalog rows — confirmed large,
composite-type OID resolution has zero schema-namespace scoping anywhere in
`catalog.InMemory`, do not attempt as a single-loop bounded fix). OR pick a
different milestone entirely for continued variety.

Gates run: go build, go vet, go test (executor full package + targeted),
tpch-spotcheck, pgbench smoke, ralph-state-guard (self-repaired) — all PASS.
In-flight: none
