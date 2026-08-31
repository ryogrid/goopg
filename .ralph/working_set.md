(idle — nothing in flight)

## Loop #8 (2026-09-01) result — M0134-0187 (`generated_stored.sql`) contained fixes shipped

**Nightly triage:** `ci/logs/action-items.md` still shows run `20260901-010436`
(7 items) — all already filed (5 pre-existing M-NIGHTLY rows re-failed, 2 new
`pg_stat_activity`-family rows -005/-007 filed loop #7). No new items this
loop.

**Task:** M0134-0187 — `generated_stored.sql`. Sized live for the first time:
0/1 PASS, 1675 diff lines, 72 `^+ERROR`. Landed **3 contained fixes** in the
shared VALUES-form INSERT/generated-column path (commit `440a23d62`), taking
it to 1608 diff lines / 59 `^+ERROR`. CSV stays `failed`/`pass_required=no`
(not close to passing — 6 more independently-verified buckets ledgered).

**Fixes shipped:**
1. Implicit INSERT column list now includes `GENERATED ALWAYS AS … STORED`
   columns (matches PG's `checkInsertTargets`, which never filtered them
   out) — 3 colIndex sites in lockstep: `analyzer.resolveInsertTargetColumns`,
   `planner.rewriteInsertDefaultMarkers`, `planner.planInsert`'s VALUES
   branch (`INSERT...SELECT` implicit-list form deliberately left alone,
   gated `s.Select == nil`). Plus new 428C9 "cannot insert a non-DEFAULT
   value into column" check. Fixed 14× fabricated "INSERT row has N values,
   target expects M" errors.
2. `computeGeneratedColumns` (operators_storage.go) moved from
   just-before-partition-routing to just-after before-insert-triggers /
   before NOT NULL+CHECK, matching upstream `ExecBRInsertTriggers` ->
   `ExecComputeStoredGenerated` -> `ExecConstraints` order.
3. `evalGenFuncCall` (operators_generated.go) gained `nullif`/`coalesce`
   arms — previously silently fell through to `NullDatum, nil`.

**Remaining gaps (ledgered, `.ralph/deferral_ledger.md` M0134-0187 row):**
(a) `information_schema.columns`/`.column_column_usage` missing entirely;
(b) generated-column CREATE-TABLE-time semantic validation absent (7 distinct
PG errors: duplicate/self-ref/whole-row-var/invalid-ref/immutability/
DEFAULT-conflict/IDENTITY-conflict); (c) whole-row `Var` eval inside CHECK
constraints; (d) INSERT-through-VIEW bypasses the new 428C9 check
(`rewriteInsertDefaultMarkers` doesn't follow the view→base chain
`planInsert` resolves); (e) `LIKE INCLUDING GENERATED` + dropped-column
source table; (f) misc (GRANT/REVOKE, extended statistics, a domain/function
case). None attempted — each independently scoped, buckets (a)/(b) are their
own REFACTOR-tier subsystem additions.

**Gates run:** `go build ./...` clean; `go test ./internal/optimizer/...
./internal/parser/... ./internal/parser/analyzer/... ./internal/catalog/...
./internal/executor/...` all PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=34); `make ralph-state-guard` PASS (one auto-repair, same benign
pre-existing "completed" marker pattern as loops #4-#7); pre-commit pgbench
smoke PASS (507/646/11721 TPS, 0 failed).

**NEXT LOOP:** Re-check banner (M0134 priority as of writing). Next unclaimed
M0134 case per ordering is **M0134-0188** (`xml.sql`, `not-tried`, never
sized) — pick that up unless the banner changes.

**In-flight:** none.
