Task just completed: M0134-0120 (encoding.sql) — sized live against PG 18.3
oracle via scripts/pg-regress-runner.sh: 0/1 PASS, 0%, 361-line diff at first
run, ending at 353-line diff / `^-ERROR` 9→8 (`^+ERROR` unchanged at 2) —
PARKED (not full pass), dominant remainder is REFACTOR-tier (LANGUAGE C
`regresslib` functions goopg stubs to NULL — no C-execution engine exists).

Landed (two contained, C-independent bugs):
- Unqualified-column fuzzy-match HINT was missing entirely: both
  `resolveColumnRefType` (internal/parser/analyzer/analyzer.go) and
  `resolveColumnRef` (internal/optimizer/planner.go) only tried the
  fuzzy-match hint on the qualified-miss path, never the unqualified
  fallback — added `suggestAnalyzerColumnHintAllRels`/
  `suggestColumnHintAllBindings`. Compounding: edit-distance-1 comparators
  measured raw bytes not runes (a 2-byte UTF-8 char counted as 2 edits) —
  rewrote both `[]rune`-based.
- `::json` cast did zero syntax validation (silent pass-through, unlike
  `::jsonb`'s sibling arm) — added `validateJSONText`
  (internal/executor/jsonb_canonical.go, reuses canonicalizeJSONB's parser
  minus re-serialization), wired into `evalCast`'s new `json` case
  (internal/executor/expr.go) and `coerceTextLikeDatum`'s new `json` branch
  (internal/executor/codec.go). ExecError.Pos left unset (PG reports this via
  DETAIL/CONTEXT not a LINE marker) — smaller follow-up noted, unfixed.

Design docs/design/m0134-0120-encoding-sizing.md, README.md indexed. CSV row
flipped not-tried → failed (pass_required=no, still parked) via
make regen-testport. Ledger row appended (.ralph/deferral_ledger.md,
2026-08-24, M0134-0120). fix_plan.md M0134-0120 marked [x] with full summary.
Commit 0f5596bc (NOT pushed — b6079bf6/70392935/96d49117/c96a9032/eb135c5b
from prior loops also still unpushed).

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0121**.

Standing recommendation (carried across several loops, still open — see prior
working_set snapshots / deferral ledger for full detail; unchanged this loop):
1. LANGUAGE C dynamic-extension loading gap — recurs across M0134-0106,
   -0118, -0120 (regresslib C functions), and create_operator/create_type
   adjacent files. Worth promoting to its own milestone.
2. Collation-execution-registry gap recurs across FIVE parked files
   (M0134-0099/-0100/-0101/-0102).
3. BETWEEN-vs-comparison-operator precedence bug (M0134-0113) — silently
   wrong for ANY query today, cross-cutting, candidate for a standalone
   bug-hunt loop.
4. RAISE INFO/LOG/DEBUG collapse to hardcoded NOTICE wire severity
   (M0134-0113, ~18-call-site blast radius in plpgsql_runtime.go).
5. `::json` cast DETAIL/CONTEXT token/context-truncation text (needs a
   json_errdetail port) — smaller follow-up from this loop, unfixed.
6. pg_shdepend-shaped object-enumeration engine (DROP OWNED BY / REASSIGN
   OWNED BY, surfaced repeatedly in M0134-0117/-0118).

Gates run this loop (subagent-reported): go build ./... clean; go test
./internal/parser/... ./internal/executor/... ./internal/postmaster/...
./internal/catalog/... ./internal/utils/... ./internal/optimizer/... PASS;
RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh PASS; pre-commit
hook's pgbench smoke ran automatically at commit time — PASS. No
planner/optimizer cost-model change (touched planner.go/analyzer.go only for
hint text, not cost/plan shape), so tpch-spotcheck.sh was not required.

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree again this loop
(.ralph/progress.json, .ralphrc, analysis/*, ci/logs/*, docs/wiki/*,
internal/executor/operators_recursive_cte.go, third-party/tpcds-postgres,
untracked `postgres` symlink) and was deliberately left untouched/
uncommitted — only the M0134-0120 files were staged and committed by
explicit pathspec.

M-NIGHTLY: checked ci/logs/action-items.md this loop — both current items
(AI-20260824-013441-001, -002) were already filed in fix_plan.md from a prior
loop (item -001 marked [x], matching the peer WIP above; item -002 is a
repeat of the already-open AI-20260822-001356-003 row). Filing obligation
satisfied, nothing new to file. Neither item blocks a gate M0134 depends on,
so M0134 selection proceeded per the banner.
