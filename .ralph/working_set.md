# Working set — 2026-09-23 (loop after M0145-0005 slice 7)

`Task:` M0145-0005 — single-pass DP over the jointree. **Slice 7 landed**
(Phase A/B retirement on the jointree arm): the S5a gate in planner.go
is `!jointree`-guarded, so `runJoinSearchBelowPinned` + the pinned spine
+ the splice/re-resolution family (`spliceSearchedSpine`, `layoutPosMap`,
`remapByPosMap`, `remapSublinkOuterRefs`, `remapOuterRefsInSubplan`) are
legacy-arm-only until the M0145-0008 cutover. New census route
`jointree-posthoc` + test-visible `sublinkRouteCounts`.

`Files:` internal/optimizer/{planner,nlicensus,predp,joinsearchseam}.go,
joinsearch_m0145_test.go (TestJointreeArmBypassesThePinnedSpineRoute),
docs/design/0100-0149/m0145-0005-*.md (new "Slice 7" section),
.ralph/fix_plan.md (nested bullet).

`Key symbols:` S5a gate `unnestPreDPEnabled() && !jointree && ...`
(planner.go:~1909), `spineRoutePosthoc`, `sublinkRouteCounts`,
`countSublinksInExpr`.

`Hypothesis/Findings:` the pinned-spine route was load-bearing
(knob-arm census `pinned-spine=285` vs `jointree-pullup=23` — it caught
every declined-all pull-up), so retirement was measured first: a probe
build skipping S5a on the jointree arm captured SF0.25 knob-arm EXPLAIN
99/99 plan-identical to the pre-gate baseline — Phase A searched the
same problem the Filter arm does and post-hoc unnest pins the same
spine Phase B's splice produced. Post-change sweep confirms
`pinned-spine=0` on the knob arm (legacy unchanged at 207).

`Next step:` commit + push (this loop). Then per banner: M0145-0005
remains `[ ]` with only slice-5 ledgered families left
(`outer-over-derived` post-B-06, `lateral`); banner order continues
(0007, 0008, 0010, M0145-0018).

`Gates run:` optimizer suite PASS; units PASS; tpch-spotcheck PASS
(Q12=2, Q13=33); SF0.25 sweep PASS=96 MISMATCH=0 TIMEOUT=0, knob-arm
plans 99/99 vs pre-gate; tpch-acceptance-arm PGSHAPED=1 24 MATCH PASS
(vs tmp/m0122-0015-arm.txt); tpcds-fireset PASS (sf025+sf1,
introduced=none).

`In-flight:` none — all gates consumed.
