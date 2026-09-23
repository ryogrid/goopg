# Working Set — loop #13 end-state

Task: M0145-0007 slice 4 re-adjudication + M0145-0004 close-out. Both done.

Files:
- `.ralph/fix_plan.md` — 0004 marked `[x]` (headline via 0004a, residuals
  ledgered); 0007 gained the "Slice-4 RE-ADJUDICATED 2026-09-23" bullets.
- `docs/design/0100-0149/m0145-0007-single-path-node-lowering.md` — Status,
  slice-plan row 4, new RE-ADJUDICATED section.
- `docs/design/README.md` — m0145-0007 index cell rewritten.

Key symbols: `runJoinSearchBelowPinned` (sole caller planner.go:1932 under
`!jointree`); `spliceSearchedSpine`/`layoutPosMap`/`remapByPosMap`/
`remapSublinkOuterRefs` (predp.go only); `unnestSubqueriesInPlan`
(posthoc site planner.go:2075); `SUBLINKCENSUS`/`PULLUPCENSUS` (nlicensus.go).

Hypothesis/Findings:
- The 2026-09-21 slice-4 blocker (291/308 pinned-spine, "blocked on
  M0145-0003 coverage") predates 0005 slice 7 (`15fbcf78d`). Fresh census,
  both corpora both arms: knob arm pinned-spine = **0** (SF0.25: 23 pullup /
  22 posthoc; TPC-H: 5 / 4); default arm unchanged 207 / 16 (control).
- Old denominator inflated: legacy note fired per WHERE-bearing statement;
  jointree notes fire only with sublinks present. Honest sublink-bearing
  events: SF0.25 = 45 (51% pulled), TPC-H = 9 (56%).
- `applyJoinTreePosMap`, `remapWithBindings`, `remapPosMapAfterRewrite`,
  `remapExprRefsToMHJ` exist only in comments — absorbed since recon.
- `reresolveJoinByName`/`remapOuterRefsInSubplan` are shared machinery for
  the posthoc route on BOTH arms — not splice leftovers.
- Verdict: slice 4 = NO-FOLD. predp.go members die wholesale at M0145-0008;
  remaining 0007 work = slice 5 (`fillJoinHashKeys`) only.

Next step: commit doc/plan updates and push; next loop selects per the
banner (M0145-0007 slice 5 or a re-pin).

Gates run: 4× `jointree-parity-capture` (sf025+tpch × knob+default) PASS;
census + caller-trace greps. No code changed — measurement/docs only.

In-flight: none.
