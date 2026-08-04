(idle — nothing in flight)

Last loop: M0127-P5.6-f-iii — DONE (docs-only), committed, pushed. Facts the
next loop should NOT re-derive:

1. **The DS05 TIMEOUT "hop" was NOT the sweep-tail confound.** It was moved by
   `ce027cee` (P5.6-f). Proven four ways: an 8-sweep/4-sweep step function
   (±3 s within regime); Q47 runs at position **47, before Q72**, so the
   confound cannot reach it in either regime; solo quiet-host runs at
   `TIMEOUT_SEC=900` give **Q47 523 s / Q57 81 s** outside any sweep tail
   (hypothesis predicted ≈31 s); and a bisect on a COPY of the cluster gives
   `30293f78` 31 s, `29daeb72` 30 s **byte-identical plan**, HEAD 523 s.
   Old binary + today's data = fast ⇒ the cluster data is exonerated too.
2. **Read a sweep header's `diff=` field BEFORE its commit subject.** The
   boundary sweep is labelled `29daeb72` but its header says `[tree DIRTY in
   Go sources]` / `diff=129e691bd41a` — that binary was `29daeb72` +
   uncommitted P5.6-f WIP. `29daeb72` itself is fully exonerated.
3. **Mechanism**: P5.6-f folds every equi-pair under INDEPENDENCE
   (`cardinality.go:457-483`, `sel /= pairNDistinct` multiplied across pairs).
   Q47's outermost join has 5 pairs, two correlated (`i_category`↔`i_brand`,
   `s_store_name`↔`s_company_name`) ⇒ degrades from a 5-pair `Hash Join` to a
   `Nested Loop` with NO join condition. Inverse of the single-key degeneracy
   trap. **P5.6-f STAYS** — net win (+Q72, +Q53, +Q9), correctness unmoved.
4. **A summary count cannot detect a traded timeout.** `TIMEOUT=1` was
   byte-identical across a 17× re-pricing for four sweeps.
5. Q47/Q57's slowdown is **cost, not correctness** — `PASS=94 MISMATCH=0
   CKMISMATCH=0` throughout; Q47 returns its 100 oracle rows in every regime.

Method worth reusing: bisect a bench regression against a `cp -a` COPY of the
data dir (2.3 G, ~30 s) + a `git worktree` per commit — the live cluster is
never at risk and code becomes the only variable. Also: solo-run the victim at
a RAISED timeout; a query clipped at the cap reports the cap, not its runtime.

Gates run: docs-only loop, no Go source touched ⇒ UNITS/SPOT/DS05 do not apply.
`make ralph-state-guard` green; commit-hook pgbench smoke green.

Nightly triage 20260805-014309: unchanged — both items (AI-…-001
IsolationEvalPlanQual, AI-…-002 pgbench/nightly) already filed under M-NIGHTLY
and left unchecked per the banner. No new nightly run since.

Next step: per the banner (M0124 → M0125 → M0127), the P5.6-f/-g series is
closed except the two successors this loop filed: **M0127-P5.6-f-iv** (the
functional-dependency arm — the real fix for Q47/Q57) and **M0127-P5.6-f-v**
(named-victim TIMEOUT-set diff in the sweep report; cheap, harness-only).
**M0127-P5.6-d** stays BLOCKED — it is gated on P5.7's batch-I/O term, which is
still unchecked, so deleting the penalty now would leave the pathology
unpriced. Re-read the banner before selecting.

In-flight: none.
