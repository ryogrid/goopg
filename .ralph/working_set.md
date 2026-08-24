Task just completed: M0134-0116 (create_type.sql) — sized live against PG 18.3
oracle via scripts/pg-regress-runner.sh: PARKED (diff 417→405 lines, `^-ERROR`
17→15, `^+ERROR` 8→8 unchanged — no new false positives).

Root cause: two independent REFACTOR-tier gaps dominate the file: (1) no
`LANGUAGE C` dynamic-extension loader (standing M0134-0106 gap) blocking
`widget`/`city_budget`/`pt_in_widget` and everything downstream; (2) goopg's
`CREATE TYPE` base/shell-type arm (`internal/parser/ddl.go`'s
`parseCreateType`, `internal/executor/operators_ddl.go`'s `execCreateType`) is
a near-total stub — no option-list parsing, no I/O-function validation, no
default-value application, no pg_depend tracking, no ALTER TYPE semantics.

Landed one contained fix within that stub:
- internal/parser/ast.go: `CreateTypeStmt` gained `HasOptions bool`.
- internal/parser/ddl.go: `parseCreateType` detects whether `(` follows the
  type name (bare shell vs. base-type-with-options spelling).
- internal/executor/operators_ddl.go: bare-shell branch (`CREATE TYPE name;`)
  now errors `42710 type "%s" already exists` against any pre-existing type
  of that name, matching PG's `DefineType` (typecmds.c ~236-266) — previously
  `RegisterCompositeType` was unconditionally idempotent.

A mirror-image "options spelling requires a pre-existing shell" check was
tried and REVERTED — it fixed one more `^-ERROR` line but introduced two
`^+ERROR` false positives (`widget`/`city_budget`, created via options
spelling with no preceding bare shell, since goopg lacks PG's `CREATE
FUNCTION`-triggered auto-shell side effect), a net regression (417→422 vs.
417→405). Documented in the design doc rather than landed — a real trap for
whoever revisits this: don't re-add that check without also modeling the
auto-shell side effect first.

Design docs/design/m0134-0116-create-type-sizing.md, README.md indexed.
fix_plan.md M0134-0116 marked [x] PARKED. Ledger row appended
(.ralph/deferral_ledger.md, M0134-0116). CSV flipped not-tried→failed via
`make regen-testport`. Commit 9b03c653 (NOT pushed yet this loop — push next
loop or on request).

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0117** (database.sql).

Standing recommendation (carried across several loops, still open — see prior
working_set snapshots / deferral ledger for full detail on each; unchanged
this loop, trimmed to the highest-value entries):
1. LANGUAGE C dynamic-extension loading gap (M0134-0106) — no loader exists;
   now confirmed blocking BOTH create_operator-adjacent and create_type.sql
   C-function types. Worth promoting to its own milestone — it recurs.
2. Collation-execution-registry gap recurs across FIVE parked files
   (M0134-0099/-0100/-0101/-0102).
3. BETWEEN-vs-comparison-operator precedence bug (M0134-0113) — silently
   wrong for ANY query today, cross-cutting, candidate for a standalone
   bug-hunt loop.
4. RAISE INFO/LOG/DEBUG collapse to hardcoded NOTICE wire severity
   (M0134-0113, ~18-call-site blast radius in plpgsql_runtime.go).
5. ADMIN-OPTION/ownership enforcement for ALTER/RENAME/DROP ROLE
   (M0134-0114 remainder) — role_ddl.go doesn't consult
   operators_ddl_role_membership.go's IsAdminOfRole.
6. CREATE SCHEMA's embedded sub-command execution (M0134-0115 remainder) —
   no AST/dispatch path exists for the nested CREATE TABLE/SEQUENCE/etc.
   inside CREATE SCHEMA; REFACTOR-tier, also partially unblocks M0134-0009.
7. NEW this loop: goopg's CREATE TYPE base/shell-type executor is a
   near-total stub (M0134-0116 remainder) — no option-list parsing (input/
   output/internallength/alignment/storage/etc.), no I/O-function arg/return
   type validation, no dependency tracking for DROP CASCADE, no ALTER TYPE
   support, and no auto-shell side effect from CREATE FUNCTION referencing an
   undeclared type name. REFACTOR-tier, own milestone candidate — this is a
   bigger gap than a single regress file suggests since user-defined base
   types are a real PG feature surface.

Gates run this loop (subagent-reported): go build ./... clean; go test
./internal/parser/... ./internal/executor/... ./internal/postmaster/... PASS;
RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh PASS; pre-commit
hook's pgbench smoke ran automatically at commit time — PASS. No
executor/planner cost-model change, so tpch-spotcheck.sh was not required
(none run). make ralph-state-guard PASS this loop (auto-repaired a stale
status/progress mismatch from the prior loop's clean-exit marker — same
pattern as last loop — then confirmed consistent).

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree this loop
(.ralph/progress.json, .ralphrc, analysis/*, ci/logs/*, docs/wiki/*,
internal/executor/operators_recursive_cte.go, third-party/tpcds-postgres,
untracked `postgres` symlink) and was deliberately left untouched/
uncommitted — only the M0134-0116 files were staged and committed by
explicit pathspec by the subagent that did the work.
