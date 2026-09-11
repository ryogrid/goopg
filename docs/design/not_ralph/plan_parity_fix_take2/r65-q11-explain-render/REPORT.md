# R65 REPORT — Q11 EXPLAIN-rendering round: Sort-key OUTER_VAR expansion + InitPlan value `.col1` (2026-09-11)

SCOPE: `r65-q11-explain-render/SCOPE.md` (reviewed APPROVE-WITH-NOTES,
8 notes applied; committed `fdb1a6774`). This is the query-closing round
R63 ordered before (a) Materialize.

## 1. Result: Q11 → MATCH, 6/14/0/2, values bit-identical, P0–P4 all hold

The cut is renderer-only, two arms, in three files (+1 test file):

- `internal/executor/operators_explain.go` (+151): `childAggregateThroughFilters`
  (Aggs-section-only, fail-closed on Star/Distinct/Filter/OrderBy/
  WithinGroup/nil-arg/coordinate doubt), `expandAggOutputRef(s)`
  clone-rewrite, Sort-arm call site reusing `keyExpr` for the S18 wrap,
  Aggregate Filter call site, SubqueryExpr Arm B `(InitPlan N).col1`
  gated on `IsNonCorrelated && InitPlan` prefix.
- `internal/optimizer/walk_export.go` (+39 `CloneExprReplacingColumnRefs`,
  reuses `cloneExprRefs` + `scopeIgnore`, fail-closed ok=false).
- `internal/optimizer/flaglabels_test.go` (1-line sf05→sf025 lane fix).
- Pins: `internal/executor/explain_agg_output_ref_test.go` (4 tests:
  Sort-key expansion, pass-through unchanged, Having-Filter expansion,
  InitPlan value `.col1`).

No planner, executor, or costing line touched — P2's values gate is the
proof, and it passes 24/24.

## 2. Gate ledger (all FOREGROUND, SCOPE §5)

1. `go test ./internal/executor/ ./internal/optimizer/` green (new pins
   included, no `-count=1`); `go vet` clean.
2. `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34; first attempt exit
   137 transient — rerun clean PASS, log `/tmp/pp2/r65-spotcheck.log`).
3. **Values** `tpch-runner -digest` full sweep pre vs post binary on the
   same clone+seed (`/tmp/pp2/clone-tpch-r65` :5533) + `-diff`: **24/24
   MATCH** (`values-pre.log` vs `values-post.log`). P2 holds — zero
   executor/planner change confirmed by measurement, not by inspection.
4. **Plans** `tpch-runner -explain` A/B, cost-bearing lines excluded:
   all 20 non-cost diff lines are Sort Key / Filter / elapsed-noise
   (`explain-ab.diff`); zero node-kind/child moves. P3 holds. Moves seen:
   Q3/Q5/Q11 Sort Keys, Q11/Q15b/Q22 InitPlan `.col1`, Q18
   `sum(l_quantity) > 313` (Arm A inside an Aggregate Filter — the
   Aggregate Filter call site working as scoped).
5. **pp** (fresh capture with this tree's binary via `estimate-audit
   -plan-only`, `r65mine.plans.txt`): **6/14/0/2 vs `bench/tpch/plans-pg`,
   Q11 MATCH []** — and the same 6/14/0/2 with Q11 MATCH against a
   FRESH live-PG capture (`r65pg.plans.txt`, PG :65432): the reference
   has not moved. Rendering-bearing set R62 {Q3,Q5,Q7,Q9,Q10,Q11,Q13,
   Q16} (8) → R65 {Q7,Q9,Q10,Q13,Q16} (5): Q3/Q5 shed `rendering`, Q11
   closed, Q10 partial (MATCH but still `rendering`), Q13 key 2 and Q16
   untouched per scope. P0 + P1 hold verbatim.
6. **DS SF0.25 sweep** foreground, private lane
   (`GOOPG_BIN=tmp/goopg-sf025-bin`): **PASS=96 MISMATCH=0 CKMISMATCH=0
   ERROR=0 TIMEOUT=0 SKIP=3**; plan-shape channel 99/99 same — no TPC-DS
   query hits the two arms, so nothing to triage. P4 holds.
   (`ds-sweep.log`, reports under
   `bench/tpcds/runtime_goopg/tpcds-results-sf025/sweep-20260911-190106.txt`.)
7. **plan-gate: explicit opt-out with rationale (no re-pin).** Structural
   mode vs `warm-pin-20260905` reports 20/22 DIFFER — but the IDENTICAL
   20/22 verdict set reproduces with the PRE-cut binary on the same
   clone (`plangate-pre.txt` vs `plangate-post.txt`, per-query verdicts
   byte-identical). The DIFFER is 100% pre-existing baseline staleness
   (R62–R64 planner evolution + stats state), zero R65-attributable
   delta; re-pinning under a renderer round would bless unrelated
   planner drift. SCOPE's "DIFFER on Q11/Q3/Q5/Q10" prediction is
   superseded: in structural mode those queries already DIFFER at HEAD,
   and the cut flips no verdict.

## 3. Deviations from SCOPE (ledgered, none blocking)

1. Gate-7 discharge is opt-out, not re-pin (see §2.7) — pre/post
   identical verdict sets is stronger than a re-pin: it proves the cut
   contributes nothing to the DIFFER.
2. Estimate side-column wiggle between captures (e.g. Q9 goopg cost
   185k→103k across sessions, Q15a rows 9980→9969) — per-session
   ANALYZE-warmup stats noise, verdict-neutral (N1 side-column only);
   per-query categories identical across the two post captures.
3. `/tmp/pp2/r65/` contains a prior interrupted session's staging
   (`goopg-r65base`, `r65base/new.plans.txt`, `pp65-*`, `values-*`);
   primary evidence is the `*mine*` / `values-pre|post` / `explain-*` /
   `plangate-*` set captured with this tree's binaries. (Its `pp65-new`
   verdicts agree with mine on every per-query category.)
4. `:65433` serves foreign `tmp/goopg-bench-bin` (started 19:00 by
   another lane — left untouched); all R65 measurement used the private
   clone :5533 (now DOWN, kept for A/B).

## 4. Sequencing (user order, preserved)

After review: commit (explicit pathspec: code + pins + REPORT + TODO —
SCOPE already committed) with `-n` + push → the R63-ordered next step
is re-triaging (a) Materialize (this round was the query-closing
precondition), then #6, R61 #4, (b) Q4, R63-#1/#2, watches, P0-04.

Evidence tmp-only `/tmp/pp2/r65/` (`values-pre|post.log`,
`explain-pre|post.txt`, `explain-ab.diff`, `r65mine.plans.txt/.txt`,
`r65pg.plans.txt/.txt`, `ds-sweep.log`, `plangate-pre|post.txt`,
`r65-spotcheck.log`); clone `/tmp/pp2/clone-tpch-r65` (server DOWN,
kept); pre-cut source worktree `/tmp/pp2/r65-pre-src` (HEAD);
binaries `goopg-pre` / `goopg-r65` (md5 `740e797f…` / `7e935a61…`),
`estimate-audit-mine`, `tpch-runner`.
