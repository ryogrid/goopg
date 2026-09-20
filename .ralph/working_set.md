Task: M0144-0003b-1 — carry each set operation's SETOP rel to the link above
  (COMPLETE, inert; the enabling slice of M0144-0003b)
Files: internal/optimizer/setopbranchrel.go (NEW: setOpBranchTag,
  setOpBranchRelOf, stampSetOpBranchRel), setopbranchrel_test.go (NEW: 6
  tests), plan.go (tag embedded in SetOp/Gather/GatherMerge),
  windowsetoppaths.go (stamp on return; branch accessor swap; chain
  predicate accepts a carrier),
  docs/design/0100-0149/m0144-0003b-1-setop-chain-branch-rel.md,
  analysis/m0144/m0144-0003b-1-setop-admission-probe.txt
Key symbols: createSetOpPaths (windowsetoppaths.go:~376/401/415),
  addPartialSetOpPath (:567), setOpBranchPartialChainOK (:857),
  setOpBranchPick (:906), searchedRelOf (searchedtree.go:169, stops at
  len(kids)!=1 on :184), searchedTree tag (searchedtree.go:100-148)
Hypothesis/Findings:
  - **The parent's filed premise is factually REFUTED.** M0144-0003b said
    `addPartialSetOpPath` "admits 0/99 corpus-wide". It does not: the
    INNERMOST link of every UNION ALL chain already files a partial path
    (Q14/Q71/Q76), and on Q14/Q71 the Gather over it already WINS its link
    outright (`cand[0] kind=11`). Probe evidence saved in analysis/m0144/.
  - What actually failed is COMPOSITION, and it split into two unrelated
    causes the type census could not distinguish:
    (A) OUTER links: the branch HAS a rel but `*SetOp`/`*Gather` is opaque
        to `searchedRelOf` (a `*SetOp` has two boundary children, so the
        walk stops). FIXED here — +3 filed partials on SF0.25, -0.
    (B) BOTH branches nil-rel (Q5, Q2, Q33, Q56, Q60): the branch subtree
        never reached the search, so there is NO rel to carry. Pre-DP
        unnest wall — M0144-0003b is now `[!]` behind
        M0142-0008a-3i-lateral-route, the FIFTH task at that one gate.
  - PG has no chain at all: `pull_up_simple_union_all` (prepjointree.c:1617
    via pull_up_subqueries, planner.c:754) flattens the union into ONE
    appendrel and `add_paths_to_append_rel` (allpaths.c:1321) sees every
    branch at once.
  - **Inert**: none of the 3 new partials wins. SF0.25 `same=99 changed=0`,
    verdict-changes=none. Movement: none.
  - TRAP AVOIDED: TPC-H cannot test this at all — zero `Append`/`SetOp`
    nodes across all 22 plans. Do not cite a TPC-H byte-diff as parity
    evidence for a set-op change; cite the structural fact and run TPC-H as
    a regression check only.
  - TRAP: `scripts/tpch-estimate-audit-arm.sh` needs `REFERENCE=` when the
    default reference capture is missing (rc=2, "no such file"), and it
    plans SERIALLY (max_parallel_workers_per_gather=0) even under
    PLAN_ONLY=1 — its output is NOT comparable to an `on-parallel` capture.
  - TRAP: `scripts/tpch-acceptance-arm.sh` takes `<arm-name> <out-file>`;
    bare invocation exits with a usage error and stamps FAIL.
  - TRAP: gate stamps hash the STAGED tree — stage before running
    tpch-spotcheck or it stamps FAIL on a PASS verdict.
Next step: banner item 2's open impl tasks are now exhausted down to the
  route-order gate. **M0142-0008a-3i-lateral-route is the single blocker
  for FIVE tasks** (M0142-0008a-3 increments (i) and (ii),
  M0142-0008a-3i-lateral, M0144-0003a, M0144-0003b) and is the highest-value
  thing left on item 2 — per S4 the lineage budget argues for escalating the
  root rather than filing a sixth descendant. Next loop should either take
  the route-order work directly or move to the next banner item.
  Do NOT touch M0144-0011 ([!]) or M0137-0019a ([!]).
Gates run: units PASS (exit 0); tpch-spotcheck PASS (Q12=2 Q13=33, stamped
  PASS against the staged tree); tpcds-sf025 sweep PASS (PASS=96 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3; plan-shape same=99 changed=0;
  verdict-changes=none); tpch-acceptance-arm rc=0 (NO-COMPARE, no baseline);
  TPC-H plan capture clean; private-lane A/B EXPLAIN sweep byte-identical;
  pgbench smoke via the commit hook.
In-flight: none. Throwaway probe fully reverted before staging.
