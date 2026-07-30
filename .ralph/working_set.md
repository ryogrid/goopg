(idle — nothing in flight)

Loop #9 (2026-07-31) took the shared CTE/outer-join arm of **M0125-0034 +
M0125-0035** that the banner named, and it landed the OUTER-JOIN half.
Committed + pushed.

`internal/planner/inner_join_qual_pushdown.go`: `joinRestrictionSides` (new,
the single policy site) + `pushConjunctIntoSubtree` (new, replaces the
one-level attach in `pushInnerJoinInputQuals`). Tests in the sibling
`_test.go` (old `…DeclinesOnOuterJoin` pin narrowed to
`…DeclinesOnUnpreservedJoin`; three new pins). Design
`docs/design/0125-0035a-preserved-side-restriction-descent.md`, evidence
`analysis/m0125-0035a-preserved-side-descent/`.

Two restrictions retired: INNER-only → preserved-side-only (a restriction
naming only the preserved side is safe with NO nullingrels model; nullable
side / FULL / SEMI / ANTI still decline; CROSS admitted), and
immediate-child-only → descent to the deepest holder.

Four findings the next loop should not rediscover:
1. **The two open items SEPARATED, they did not converge.** -0035's remainder
   is a CTE-BODY problem (needs single-reference `cte_inline` + EC constant
   propagation for Q78); -0034's remainder is a JOIN-ORDER problem.
2. **-0034's stated starting point is REFUTED by measurement.** CROSS was
   admitted to the pass and ZERO of the 8 crosses moved. Q64 places
   `date_dim d2`/`d3` before the `customer` their equi-predicate needs, so
   `pushOneConjunct` is correct to decline a side-spanning conjunct. Resume in
   `joinorder.go`/`bushy.go` (collapse/greedy threshold vs FROM order).
3. **Q18's TIMEOUT → PASS is NOT this change** — its plan is byte-identical;
   loop #8's reading was contaminated by the live nightly. Stays `M0125-0033`.
4. `tmp/goopg-m0125-0035-bin` is loop #8's binary = the clean A arm for any
   A/B against this commit.

Gates (host verified QUIET all loop — nightly ended 04:08): units PASS;
`tpch-spotcheck.sh` `RESULT=PASS` (Q12=2 Q13=35); TPC-H plan-diff vs
`m0125-0035-c2-qual-placement` **1/22 (Q17), zero structural change**; timed
w2 A/B **neutral** (395.5 → 389.1 s, identical rows on all 21 completers);
full 99-query SF0.5 gate **PASS=89 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=6
SKIP=4**, all 87 common PASSes identical in status AND checksum.

Per the banner the next selection is **`M0125-0036`** (C3, uncached correlated
SubPlan). Re-read the banner first; it outranks this note.

Clusters left DOWN as found; :65438 (PG) was already UP and is left UP.
Filed this loop, unworked per the banner: M-NIGHTLY
`testport/TestE2E_FailoverGoopgToPG` (AI-20260731-001201-001).
