# M0146-0015d — `j->larg` arm for nested sublink pull-up (impl evidence)

Design: `docs/design/0100-0149/m0146-0015b-nested-sublink-larg-pull.md`
(recon: `analysis/m0146/m0146-0015b/`).

## What landed

`extractNestedPullups` tries an EXISTS/NOT-EXISTS nested body against the
enclosing problem ctx (`bodyCtx.parent`, PG's jtlink1) before the parent
body ctx (`bodyCtx`, jtlink2). Larg-bound children live in a separate
`jtPulledBody.largChildren` list, flatten immediately before their parent
(stamped with the parent's own `parent`), and the parent's `sjLeft`
widens by their leaves in `classifyPulledQuals`.

**Deviation from the filed spec: the ANY arm was tried and removed.**
`outerOperandAsLevel1` binds operand refs by column name; a parent-scope
operand can name a column that also exists on an emitting relation, so
the name resolves to the wrong leaf. TPC-DS Q83 (SF0.25) crashed in
`createPlan`/`translateToLayout` ("join clause references binding column
4 (d_week_seq), which is not among the 6 output columns"). PG's varno-
based `IncrementVarSublevelsUp` cannot misresolve; goopg's name binding
can, so `pullUpAnyBody` keeps the rarg arm only. Regression pinned by
`TestJointreePullupNestedLargArm/ANY_operand_bound_in_the_parent_scope_never_larg-binds`.

## Files

- `canonical-explain.txt` — regress `subselect` nested EXISTS/NOT EXISTS
  on tenk1: `Merge Join (a=b)` over `Nested Loop Anti Join (a,d)` (larg)
  and `Nested Loop Semi Join (b,c)` (rarg) — PG's insertion structure.
- `canonical-rows.txt` — 0 rows in ~0.23 s (kept-SubPlan shape: ~197 s).
- `canonical-census.txt` — `route=jointree-pullup` + `decline=(pulled)`
  for both converted sublinks; the `no-level1-correlation` lines are the
  inner pull declined inside the parent body's own planning (Level-2
  there — expected).
- `synthetic-correctness.txt` — positive-data checks (s_a..s_d):
  base d={2} → {1}; d={1,2} → ∅; d=∅ + c(200) → {1,2}.
- `sf025-sweep.txt`, `sf025-flow.log` — TPC-DS SF0.25: PASS=96,
  MISMATCH=0, ERROR=0, TIMEOUT=0; `jointree-pullup=23` = pre-change
  baseline (221745's 19 was the Q83 crash truncating the census);
  plans-20260926-222349 bodies byte-identical to baseline
  plans-20260926-213404 (header-only diff).
- `arm-on.txt`, `arm-on.txt.diff-vs-baseline.txt` — TPC-H acceptance arm
  vs `tmp/m0145-0008m/arm-on.txt`: `SUMMARY: 24 MATCH`, `VERDICT: PASS`.
- `fireset/` — TPC-DS SF0.25 + SF1 baseline-vs-candidate fire reports:
  no fires, parity reports identical.
- `m0146-0015d-tpch-diff.txt`, `m0146-0015d-tpch-class.txt` — TPC-H
  plan-parity capture: `match=6 shapediff=16` — identical to the
  pre-change baseline (floor 3, stable 6/22).

## Gates

- `go test ./internal/optimizer/` — PASS (incl. the 6 new
  `TestJointreePullupNestedLargArm` subtests).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — PASS.
- `scripts/tpch-spotcheck.sh` — Q12=2, Q13=33 PASS.
- `go test -run 'TestPort_RegressSuite$' ./internal/testport/` — PASS
  (291 s, capped run).

## Bug found and fixed during the loop

Q83 crash under the first draft (ANY larg arm): server panic, sweep
PASS=95 + ERROR=1, census dropped to 19. Fix = drop the ANY arm
(unsound per name-binding, above); Q83 then returns 1 row, no crash,
plan byte-identical to the pre-change baseline.
