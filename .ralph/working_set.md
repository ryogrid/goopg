(idle — nothing in flight)

## Loop summary (2026-07-10, loop #11)

**Outcome: M0122-0023 — EXCLUDE USING gist (col WITH &&) DDL-time
type-validation gap fixed and closed in unimplemented_feat.json. Real
bugfix (not just a stale-audit verify), plus new regression coverage
for a previously-completely-untested enforcement path. Committed
(pathspec-scoped, peer-safe) and pushed.**

- Nightly triage: sole action item AI-20260710-011513-001 (build failure)
  already had a closed M-NIGHTLY task from loop #11's predecessor (loop
  where the entry appeared, closed same day); `go build ./...` clean at
  loop start — no new M-NIGHTLY task needed.
- Continued loop #10's survey list: picked #93 `EXCLUDE USING` type-
  validation bypass (`createExclusionIndexStub`,
  `internal/executor/operators_ddl.go:9882`).
- Investigation found the JSON entry was PARTLY stale: `EXCLUDE USING
  btree (col WITH =)` was ALREADY fully type-validated (routes through
  `createBTreeIndex` → `isSupportedBTreeKeyType`). The REAL, still-open
  gap was narrower: `EXCLUDE USING gist (col WITH &&)` accepted ANY
  column type with zero validation. Wrote a throwaway probe test
  (`newDDLFixture` + `runDDL`) to check actual runtime behavior —
  discovered `checkGistOverlapExclusion`
  (`internal/executor/operators_storage.go:7257`) only understands
  `box` values (via `parseBoxText`), so a non-box `&&` exclusion
  constraint was silently accepted at DDL time and then NEVER fired at
  INSERT time (fails closed, no error, ever) — worse than rejecting it
  up front. Also discovered (fixing my own probe test's wrong literal)
  that PG's real box I/O format is `(x1,y1),(x2,y2)` — NO outer
  wrapping parens (verified against
  `postgres/src/backend/utils/adt/geo_ops.c`'s `path_encode`/`box_out`)
  — and that the box/box overlap enforcement itself DOES work
  correctly once given a correctly-formatted literal, but had ZERO
  test coverage before this loop.
- Fix: `createExclusionIndexStub` now rejects `&&` on a non-`box`
  column at DDL time with `42704` ("data type %s has no default
  operator class for access method %q"), matching PostgreSQL's real
  `indexcmds.c ResolveOpClass` rejection (verified against
  `postgres/src/backend/commands/indexcmds.c:2272-2277`).
- Added `TestExclusionConstraintGistOverlapFires` (box/box overlap +
  non-overlap negative case) and
  `TestExclusionConstraintGistOverlapRejectsUnsupportedType` to
  `internal/executor/exclusion_constraint_test.go`.
- Flipped `unimplemented_feat.json`'s matching entry `open`→`resolved`
  via surgical 2-line `Edit` (84/181 resolved, 97 open).
- Added `.ralph/fix_plan.md` entry `M0122-0023`.
- Added a `.ralph/deferral_ledger.md` row: remaining scope (real GiST
  access method, point/circle/polygon overlap types, general
  opclass/operator-family resolution for other exclusion operators)
  is out of bounds for a single loop — stays tracked under
  `unimplemented_feat.json` #118 (GIST index support).
- Updated `docs/design/0119-0004-deferred-exclusion.md` (addendum
  section — this is the design doc that already documents the
  `checkGistOverlapExclusion`/`recheckDeferredExclusionOverlap`
  enforcement chokepoints my fix gates) and its `docs/design/README.md`
  index row (appended, did not rewrite the existing long summary).
- Gates run (foreground, all PASS): `go build ./...`, `go vet ./...`;
  `go test ./internal/executor/... -run TestExclusionConstraint` (4/4);
  `go test ./internal/executor/...` (full package); JSON validity check;
  `scripts/tpch-spotcheck.sh` (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
  bash scripts/ralph-precommit-test.sh` (0 failed, all 3 workloads);
  `make ralph-state-guard` (self-repaired a stale running/completed
  mismatch, same pattern as loops #7-#10).
- Committed, pathspec-scoped to `.ralph/deferral_ledger.md`,
  `.ralph/fix_plan.md`, `.ralph/progress.json`,
  `docs/design/0119-0004-deferred-exclusion.md`, `docs/design/README.md`,
  `internal/executor/exclusion_constraint_test.go`,
  `internal/executor/operators_ddl.go`, `unimplemented_feat.json` — did
  NOT touch the concurrently-modified `ci/logs/*`,
  `analysis/tpch-explain-baseline.md`, `postgres` submodule, or
  untracked `weekly_loc.*` files sitting in the tree from another
  process. Pushed to `origin/align-data-structure-with-pg`.

**Next natural work:** continue surveying `unimplemented_feat.json`'s
remaining ~97 open entries for more candidates. Two carried over from
loop #10's survey are still CONFIRMED genuinely open (not yet picked
up): #67 `pg_get_expr()` (pass-through-only, no real node-tree
reconstruction — likely fine in practice since every populated
pg_node_tree column already stores pre-formatted text, but worth a
closer architecture review before declaring done), #88 `WITH OPTIONS`
column clause (parser literally no-ops the tokens; the *unrelated*
generated-column-override feature for `PARTITION OF` already works via
`ColGeneratedExprs`, so rescope narrowly if picked up). OR M0122-0007's
remaining real scope (index/typed-table TEMPLATE copying — confirmed
large, do not attempt as a single-loop bounded fix). OR pick a
different milestone entirely for continued variety.

Gates run: go build, go vet, go test (executor full package + targeted),
tpch-spotcheck, pgbench smoke, ralph-state-guard (self-repaired) — all PASS.
In-flight: none
