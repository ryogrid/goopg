Task just completed: M0134-0119 (drop_operator.sql) — sized live against PG 18.3
oracle via scripts/pg-regress-runner.sh: FULL PASS (30-line/0%-parity diff →
byte-identical, 57-line output, no PARK needed).

Landed:
- internal/catalog/catalog.go: `builtinProcsByName` was missing `int8eq`/
  `int8ne`/`int8lt`/`int8gt` (PG's real pg_proc.dat OIDs 467-470). The lookup
  miss made CREATE OPERATOR's boolean-return-for-negator validation see an
  empty return type and misfire "only boolean operators can have negators"
  against a genuinely-bool-returning proc.
- catalog.InMemory.DropUserOperator: ported PG's delete-time
  `OperatorUpd(operOid, oprcom, oprnegate, true)`
  (postgres/src/backend/catalog/pg_operator.c ~671-820) via new
  `userOperatorByOIDLocked` helper — previously a sibling operator's
  CommutatorOID/NegatorOID cross-reference was never cleared when the
  operator it pointed at was dropped, leaving pg_operator.oprcom/oprnegate
  dangling at a freed OID (exactly what the test's two catalog-integrity
  NOT EXISTS(...) checks catch).

Design docs/design/m0134-0119-drop-operator-sizing.md, README.md indexed.
CSV row flipped not-tried → pass, pass_required=yes (full pass, unlike the
preceding M0134-0114..-0118 PARK streak). Ledger row appended
(.ralph/deferral_ledger.md, M0134-0119). fix_plan.md M0134-0119 marked [x].
Commit 70392935 (NOT pushed yet — eb135c5b/241c157f/9b03c653/c96a9032/
96d49117 from prior loops are also still unpushed).

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0120** (encoding.sql).

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
   datacl = ...` rejected outright, and `REASSIGN OWNED BY` has zero parser
   support (same gap surfaced again in M0134-0118's DROP OWNED BY/REASSIGN
   OWNED BY note — worth promoting to its own milestone, needs a
   pg_shdepend-shaped object-enumeration engine spanning every ownable
   catalog kind).

Gates run this loop (subagent-reported): go build ./... clean; go test
./internal/parser/... ./internal/executor/... ./internal/postmaster/...
./internal/catalog/... PASS; RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh PASS; pre-commit hook's pgbench smoke ran
automatically at commit time — PASS. No planner/optimizer change, so
tpch-spotcheck.sh was not required (none run). make ralph-state-guard PASS
this loop (auto-repaired the same recurring stale status/progress
clean-exit-marker mismatch seen in prior loops, then confirmed consistent).

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree this loop
(.ralph/progress.json, .ralphrc, analysis/*, ci/logs/*, docs/wiki/*,
internal/executor/operators_recursive_cte.go, third-party/tpcds-postgres,
untracked `postgres` symlink) and was deliberately left untouched/
uncommitted — only the M0134-0119 files were staged and committed by
explicit pathspec by the subagent that did the work. That peer WIP file
(operators_recursive_cte.go) matches the already-`[x]`-marked M-NIGHTLY item
AI-20260824-013441-001's described fix verbatim — likely a concurrent loop's
in-progress commit, not a new discovery; leave it for that loop to land.

M-NIGHTLY: checked ci/logs/action-items.md this loop — both current items
(AI-20260824-013441-001, -002) were already filed in fix_plan.md from a
prior loop (item -001 already marked [x] fixed there, matching the uncommitted
peer WIP noted above; item -002 is a repeat of the already-open
AI-20260822-001356-003 row). Filing obligation satisfied, nothing new to file.
