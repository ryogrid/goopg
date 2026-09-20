Task: M0142-0008a-3i-lateral-route — recon COMPLETE. The filed framing was
  insufficient; the route defect is one stage EARLIER than any phase.
Files: docs/design/0100-0149/m0142-0008a-3i-lateral-route-recon.md (NEW),
  docs/design/README.md, .ralph/fix_plan.md, .ralph/deferral_ledger.md.
  NO production file touched (recon).
Key symbols: planner.go:1499 `resolveExpr(whereQual, ctx)` → planExistsExpr
  (:15320-15333) → planSelectWithParent (:15661); planner.go:1539
  `unnestSubqueriesInPlan`; :1540 `runJoinSearchBelowPinned` (predp.go:78,
  Phase A tryJoinSearch :152, Phase B tryPGShapedJoinSearch :195);
  ExistsExpr/InExpr structs (plan.go) — `Plan Node`, NO parser subquery.
Hypothesis/Findings:
  - **Reordering Phase A and Phase B cannot fix anything.** The lowering
    happens before EITHER phase: `resolveExpr` on the WHERE clause runs a
    FULL recursive planner run on every EXISTS/IN body and keeps only the
    finished Node. The parse tree is dropped and unreachable afterwards.
  - This is the one object all four prior probes hit from different angles
    (`-lateral`, `-leafcount`, M0144-0003a's census, its refutation). Each
    attributed it to the phase it happened to be inspecting.
  - PG inverts the order: `pull_up_sublinks` (planner.c:737) and
    `pull_up_subqueries` (:759) run while the body is still an unplanned
    `Query`; `SS_process_sublinks` (:1328) plans ONLY what pull-up refused;
    `query_planner` (:1654) searches one flattened range table.
  - **All five witnesses are flattenable**: Q16/Q94 = one `catalog_sales`;
    Q35/Q10/Q69 = `store_sales,date_dim` (+ web/catalog siblings). Plain
    SELECTs, no agg/HAVING/window/setop/DISTINCT/LIMIT/ORDER BY — all
    satisfy `is_simple_subquery` (prepjointree.c:1807) in full.
  - Route filed as **M0142-0008a-3i-route-a** (impl): (1) retain the parser
    subquery on ExistsExpr/InExpr — additive, 7 call sites, 52 `.Plan`
    readers unaffected; (2) flatten a simple body into the outer join list
    before planning it. Changes WHEN a body is planned = planner-pipeline
    work, not seam/admission work.
  - Tracker corrections made this loop (stale checkboxes, no work implied):
    M0144-0003a `[ ]`→`[!]` (its own notes say unimplementable),
    M0144-0011b `[ ]`→`[x]` (recon complete since loop #47),
    M0141-S2a-fix `[ ]`→`[x]` (fix1 `[x]`, fix2/fix2r `[x]` — both halves).
Next step: **M0142-0008a-3i-route-a** is the implementable successor and is
  the one task that unblocks all five. SIZE STEP 1 ALONE FIRST — carrying
  the parser subquery is additive and measurable on its own (it must be
  provably inert), and only then attempt the flattening splice. Do NOT
  re-attempt a seam widening: `chainCarriesLateral`, the `*Project`
  descent, and the synthetic RHS participant are each measured and refuted.
  Do NOT touch M0144-0011 ([!]), M0137-0019a ([!]), M0142-0005 ([!]).
Gates run: units PASS (exit 0, 0 FAIL); pgbench smoke via the commit hook.
  No values/plan gate applies — zero internal/ or cmd/ files changed, so no
  plan can move (the recon's whole output is documents).
In-flight: none.
