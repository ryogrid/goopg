Task just completed: M0134-0118 (dependency.sql) — sized live against PG 18.3
oracle via scripts/pg-regress-runner.sh: PARKED (diff 187→156 lines, `^-ERROR`
10→5, `^+ERROR` 16→14, no new false positives).

Landed:
- internal/catalog/catalog.go: `InMemory.RoleDropDependencyDescriptions` —
  bounded port of PG's `checkSharedDependencies` (pg_shdepend.c), scanning
  the shared `tableACLs` OID-keyed store for table/database ACL grants and
  `AllTables()` for table ownership.
- internal/executor/operators_ddl.go: wired into `execDropCompat`'s role arm
  (NOT role_ddl.go's `tryHandleRoleDDL` — live verification showed that path
  is dead code for DROP ROLE/USER/GROUP; parses as generic DropCompatStmt,
  runs entirely through the executor).
- internal/postmaster/grant_ddl.go: new `splitRoleGrantList` — fixed a
  second bug found live: `GRANT ... TO GROUP <role>` recorded the grant
  under literal string "group <role>" instead of "<role>" (role-list
  splitter had no handling of gram.y's legacy per-item GROUP keyword);
  swapped in at all 10 call sites.

Debugging pitfall recorded: `tableACLs[oid][role][priv]`'s boolean records
WITH GRANT OPTION, not "granted at all" — map-key presence is the grant.

Design docs/design/m0134-0118-dependency-sizing.md, README.md indexed.
fix_plan.md M0134-0118 marked [x] PARKED. Ledger row appended
(.ralph/deferral_ledger.md, M0134-0118: DROP OWNED BY/REASSIGN OWNED BY
entirely unparsed; RoleDropDependencyDescriptions covers only table
ACLs/ownership, not schema/function/type/sequence/default-privilege
ownership, and uses OID-order not PG's true shared_dependency_comparator
sort). CSV flipped not-tried→failed via `make regen-testport`. Commit
c96a9032 (NOT pushed yet — push next loop or on request; eb135c5b/241c157f/
9b03c653 from prior loops are also still unpushed).

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0119** (drop_operator.sql).

Standing recommendation (carried across several loops, still open — see prior
working_set snapshots / deferral ledger for full detail on each; unchanged
this loop, trimmed to the highest-value entries):
1. LANGUAGE C dynamic-extension loading gap (M0134-0106) — no loader exists;
   confirmed blocking create_operator-adjacent and create_type.sql C-function
   types. Worth promoting to its own milestone — it recurs.
2. Collation-execution-registry gap recurs across FIVE parked files
   (M0134-0099/-0100/-0101/-0102).
3. BETWEEN-vs-comparison-operator precedence bug (M0134-0113) — silently
   wrong for ANY query today, cross-cutting, candidate for a standalone
   bug-hunt loop.
4. RAISE INFO/LOG/DEBUG collapse to hardcoded NOTICE wire severity
   (M0134-0113, ~18-call-site blast radius in plpgsql_runtime.go).
5. ADMIN-OPTION/ownership enforcement for ALTER/RENAME/DROP ROLE
   (M0134-0114 remainder).
6. CREATE SCHEMA's embedded sub-command execution (M0134-0115 remainder) —
   REFACTOR-tier, also partially unblocks M0134-0009.
7. CREATE TYPE base/shell-type executor stub (M0134-0116 remainder) —
   REFACTOR-tier, own milestone candidate.
8. Two independent gaps in M0134-0117's remainder: `UPDATE pg_database SET
   datacl = ...` rejected outright (only datconnlimit is writable via direct
   pg_database UPDATE), and `REASSIGN OWNED BY` has zero parser support.
9. NEW this loop: `DROP OWNED BY`/`REASSIGN OWNED BY` (M0134-0118 remainder)
   — both entirely unparsed, need a pg_shdepend-shaped object-enumeration
   engine spanning every ownable catalog kind (multi-milestone REFACTOR-tier;
   overlaps item 8's REASSIGN OWNED BY note — same gap surfaced twice now
   from two different files, worth promoting to its own milestone).

Gates run this loop (subagent-reported): go build ./... clean; go test
./internal/parser/... ./internal/executor/... ./internal/postmaster/...
./internal/catalog/... PASS; RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh PASS; pre-commit hook's pgbench smoke ran
automatically at commit time — PASS. No executor/planner cost-model change,
so tpch-spotcheck.sh was not required (none run). make ralph-state-guard
PASS this loop (auto-repaired a stale status/progress mismatch from the
prior loop's clean-exit marker — same recurring pattern as prior loops —
then confirmed consistent).

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree this loop
(.ralph/progress.json, .ralphrc, analysis/*, ci/logs/*, docs/wiki/*,
internal/executor/operators_recursive_cte.go, third-party/tpcds-postgres,
untracked `postgres` symlink) and was deliberately left untouched/
uncommitted — only the M0134-0118 files were staged and committed by
explicit pathspec by the subagent that did the work.

M-NIGHTLY: no new ci/logs/action-items.md entries surfaced this loop beyond
what was already filed in prior loops — filing obligation satisfied, nothing
new to file.
