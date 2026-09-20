Task: M0142-0008a-3i-route-a — STEP 1 LANDED (inert). Step 2 (the flattening
  splice) is the remaining work; the task stays open.
Files: internal/optimizer/plan.go (ExistsExpr.Subquery, InExpr.Subquery),
  planner.go (:15306 IN, :15338 EXISTS assignments; :16834/:17072 copy sites),
  foldconst.go:69 (rebuild site — was the bug), exists_to_any.go:384
  (deliberately nil), sublinkpullup.go (NEW), sublinkpullup_test.go (NEW),
  docs/design/0100-0149/m0142-0008a-3i-route-a-retain-sublink-parse-tree.md
Key symbols: sublinkBodyIsSimple, selectListOrQualHasAggOrWindow,
  isAggregateFuncName (planner.go:9662), walkExpr (:9597),
  planExistsExpr/planInExpr, unnestSubqueriesInPlan (step 2's site),
  FoldConstants.
Hypothesis/Findings:
  - Step 1 lands the two prerequisites the route needed and NOTHING else:
    the parse tree is retained (it was consumed by planSelectWithParent and
    dropped, so there was nothing to flatten), and `sublinkBodyIsSimple`
    ports `is_simple_subquery` (prepjointree.c:1807) refusal-for-refusal.
  - **The retention test caught a real sibling-drift bug.** `FoldConstants`
    (foldconst.go:69) REBUILDS an `InExpr` field by field and silently
    dropped the new field — EXISTS passed while IN failed. Three copy sites
    now carry it; `exists_to_any.go:384` deliberately does not (it REWRITES
    rather than copies, so nil is the fail-closed answer). Write the test to
    compare POINTERS: a shape comparison would have passed a re-parse.
  - Verified the retention test FAILS with the two resolver assignments
    removed, before claiming it pins anything.
  - **Port gap recorded, not hidden**: `hasTargetSRFs` is NOT implemented —
    classifying a function as set-returning needs the catalog this predicate
    does not take, so an SRF-in-target-list body is currently ACCEPTED.
    Step 2 must take a catalog argument or refuse unclassifiable FuncCalls;
    it must NOT inherit today's answer. `security_barrier`/`lateral` arms
    are safe-by-construction for qual sublinks, not merely unported.
  - Inert by construction: zero production readers. Gates run anyway —
    "provably inert" is a claim to be checked, not a reason to skip.
  - TRAP (again): gate stamps hash the STAGED tree. The acceptance arm needs
    a fresh BASE arm built from the unmodified tree — copy the changed files
    aside, `git checkout HEAD --` them, run the base arm, restore, re-`git
    add`, then run with ACCEPT_BASELINE. `git checkout HEAD --` clobbers the
    INDEX too, so re-staging is mandatory or the stamp reads FAIL.
Next step: **step 2 — the flattening splice.** In `unnestSubqueriesInPlan`,
  test a retained body with `sublinkBodyIsSimple` and, when accepted, splice
  its FROM items into the outer join list as REAL relations with its quals
  merged into the outer predicate, discarding `.Plan`. Obligations already on
  the task: give the predicate a catalog (SRF arm) FIRST; re-derive the leaf
  arithmetic rather than carrying today's `Q16 nrels=4 nprefix=4 scans=3`;
  close the P0-H11 `cumulativeFromSpans` round-trip in the SAME change; keep
  Q78's `outer-over-derived` firewall intact; re-base `OuterColumnRef{Level:1}`
  correlation refs into the outer chain's column space.
  Do NOT touch M0144-0011 ([!]), M0137-0019a ([!]), M0142-0005 ([!]).
Gates run: units PASS (exit 0, 0 FAIL); tpch-spotcheck PASS (Q12=2 Q13=33);
  tpcds-sf025 sweep PASS (PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0
  SKIP=3; plan channel same=99 changed=0; verdict-changes=none);
  tpch-acceptance-arm PASS (24/24 on VALUES vs a fresh same-loop base arm);
  go vet clean; pgbench smoke via the commit hook.
In-flight: none.
