# R64 SCOPE — decline NLI when the probe is the preserved side (Q13 wrong-results fix, ledger R63-#3)

Follows the R63 REPORT §7 spotcheck FAIL (Q13=33 rows vs expected 34).
Bisect verdict: R63 exonerated (R63==R62 byte-identical digests+plan on
identical data), `GOOPG_MEMOIZE=off` answers 34, r59 already picks the
identical Memoize plan — a pre-existing wrong-results bug on the Memoize+NLI
path, exposed 09-03 by take2 P2-02c (`62a5006c7`, per-session
`enable_memoize`). User chose fix-first: this round lands the fix, then R63
gates are re-verified post-fix, R63 commits, then the SF0.25 migration
(user-sequenced; migration touches no analysis reports).

## 1. Mechanism chain (fully traced by reading — no probe prints needed)

Q13 is `customer LEFT JOIN orders` (sjinfo LEFT, MinL={customer},
MinR={orders}):

1. **Commuted direction** (`jointypeForDirection`, `joinpaths.go:157-210`,
   C-06s): outer=orders, inner=customer, `jt=JoinRight`. The direction is
   legal and stays legal — hash/merge need it (C-06s cites Q13's own
   1.5M-row build).
2. **`addNLIPaths` passes `jt` through verbatim** (`joinpathsnli.go:327`
   `Jointype: jt`) and offers the bare probe AND the Memoize-wrapped probe
   to the same `try_nestloop_path` slot. P2-02c made the wrapper eligible;
   costing picks it.
3. **createPlan fused arm builds
   `NestedLoopIndexJoin{Type:Right, InnerMemo}`**
   (`createplannl.go:374-417`).
4. **Executor drops the preserved side.** `nestedLoopIndexJoinOp.Next()`
   handles Semi/Anti/Left only; outer-EOF → EOF. Deeper than a missing
   sweep arm: the shape is UNWINNABLE for any driver — the preserved side
   is the parameterized probe, which by construction returns only
   customers matching some orders row. The 50,000 zero-order customers
   surface from NO probe execution, so no sweep, matched-bitmap, or
   cache-aware bookkeeping can ever emit them. Observed: 34→33 rows,
   `0|50000` gone, custdist SUM 150000→100000
   (tmp-only `/tmp/pp2/q13-memo.txt` vs `q13-nomemo.txt`; the `0|50000`
   cell independently verified by a join-free `NOT EXISTS` count = 50000).
5. **Memoize is the observed instance, not the defect.** A bare-probe
   NLI-Right drops the same rows (a sweep sees only probed rows).
   `GOOPG_MEMOIZE=off` "fixed" Q13 via costing (Hash Right Join won), not
   via correctness — a non-memoized NLI-Right winning anywhere would be
   equally wrong.

PG-faithfulness (all verified prior session): PG 18 plans Q13 as `Hash
Right Join` (:65432); PG forced-to-nestloop commutes to `Nested Loop LEFT
Join` with customer outer — PG never builds a probe-preserved NLI; the
preserved side is always the outer (full scan).

## 2. The cut

**ONE gate in `addNLIPaths`: decline `jt == parser.JoinRight`.** The
admitted set then mirrors `partialHashJoinTypeOK` (`parallel.go:874`:
Inner/Left/Semi/Anti); FULL is already excluded (`jointypeForDirection`
returns legal=false for FULL, and `planJoinTypeFor` panics on it).

Single-point coverage: all three createPlan NLI sites read the Path's
jointype via `planJoinTypeFor` — fused index (`createplannl.go:408`),
decomposed lateral `Join` (`:352`), bitmap (`:514`) — so emitting no
Right NLI path means none is ever built. The commuted direction keeps its
hash/merge paths (C-06s), so Q13 falls to Hash Right Join — PG's shape.

In-repo precedent ×3 (this gate aligns the lone outlier, it invents
nothing): `tryBuildNLI` declines RIGHT/FULL ("require both sides
materialised for outer-row preservation", `nl_index_join.go:324-327`);
`addPartialNestLoopPaths` V1 dispatch gate (`joinpathsnli.go:390-393`);
FULL declined in `jointypeForDirection`.

Deliberate non-mirrors (not drift): no executor change (unwinnable
there — §1.4); no Memoize-only gate (insufficient — §1.5); no
`jointypeForDirection` change (Right direction stays legal for
hash/merge); no GUC. Guardrail (review note 3): the gate belongs in
`addNLIPaths` ONLY — `addNestLoopPath` (`pathgen.go`) must keep admitting
Right, since plain-NL Right over a complete inner is correct via the
generic sweep; a `jt==Right` refusal at the `addPathsToJoinrel` level
would also kill the correct hash/merge-Right and plain-NL-Right paths.
Fail-closed hardening (review note 2, landed with the gate): a panic on
`jt==Right` in all three createPlan NLI consumers (fused, decomposed
lateral `Join`, bitmap) — a Right over a parameterized inner is always
wrong (the lateral stream has no `fillInner`: a `Join{Lateral,Right}`
would silently degrade to Inner), so any future producer slip becomes a
loud failure instead of dropped rows.

Unit pin (new test in `joinpathsnli_test.go`, following
`TestNLIArmAdmission`'s fixture): through `addPathsToJoinrel` with a
LEFT sjinfo — commuted direction (outer⊇MinR, inner⊇MinL, jt=Right)
emits ZERO parameterized `PathNestLoop`; forward direction (jt=Left)
still admits; Inner/Semi/Anti unchanged. `jointypeForDirection` itself
is UNCHANGED (still returns Right — hash/merge need it).

## 3. Falsifiable predictions

| # | Claim | Mechanism (§1–2) | Verdict on miss |
|---|---|---|---|
| P0 | Spotcheck Q13 33→34 (`0\|50000` present, SUM=150000); Q12 stays 2 | direction declined, Hash Right Join serves | any other count → STOP, re-audit |
| P1 | EXPLAIN Q13 → `Hash Right Join` (PG's shape); no `NestedLoopIndexJoin{Right}` anywhere in the TPC-H corpus | §2 single point | surviving NLI-Right → STOP (second producer) |
| P2 | values md5 MATCH pre-fix binary on all 22 TPC-H queries EXCEPT Q13 (33→34 fix delta); Q11 32000/10666, Q5 25 pinned | planner-only direction decline | any other movement → STOP |
| P3 | pp: exactly ONE text move (Q13 Memoize-NL-Right → Hash-Right, toward-oracle); DS SF0.5 sweep same PASS set + Q72 TIMEOUT alone, flips adjudicated per-row | the fix IS a shape move on Q13-shaped plans | unadjudicated flip → explain-or-stop |
| P4 | DPTRACE Q13 A/B vs pre-fix binary: only the join episode moves | direction-decline defect | anything else → re-audit |

Re-audit rule (R59–R63): any P-miss triggers DPTRACE A/B against the
pre-fix binary, not celebration.

## 4. Sibling audit (why the cut is one gate)

- `tryBuildNLI` (old rewrite producer): already declines Right/Full — no change.
- `addPartialNestLoopPaths` (partial producer): V1 gate already excludes Right — no change.
- `jointypeForDirection`: unchanged by design (hash/merge need the direction).
- Decomposed `Join{Lateral,Right}`: unreachable after the gate (same Path source).
- `nestedLoopIndexJoinOp`: unchanged (nothing it could do — §1.4).
- `getMemoizePath`: unchanged (the wrapper is fine for Inner/Left/Semi/Anti).

## 5. Gates (implementation round, all FOREGROUND)

1. `go test ./internal/optimizer/` green (new test included, no `-count=1`); `go vet` clean.
2. `scripts/tpch-spotcheck.sh`: Q12=2, Q13=34 (practice-card must-gate).
3. TPC-H values A/B vs pre-fix binary on identical data: 21 MATCH + Q13 33→34.
4. EXPLAIN Q13 = Hash Right Join (+ DPTRACE A/B if cheap).
5. DS SF0.5 sweep foreground, fresh build, private lane: same PASS set + Q72 TIMEOUT alone.
6. REPORT.md, then review, then commit + push. Then, user-sequenced: re-verify R63 gates post-fix → R63 commit → SF0.25 migration.

## 6. Ledgered follow-ups (not this round)

- R63-#3 consumed by this round (ledger row updated on land).
- Unchanged: R63-#1 (partial-skew over-count), R63-#2 (IOS-less sole-scan/small-side/outer-count arms), (a) Materialize (insufficient-alone), #6 (cost-model/stats), R61 #4 (fail-closed-correct), (b) Q4 (3× deferred), NLI staleness comment, R61 #5 (executor watch), R62-#1 (no live case).
- Road-not-taken record: Memoize matched-tracking for Right joins (a bigger, PG-unfaithful build for a shape PG itself never plans — declined, not deferred).

Evidence tmp-only `/tmp/pp2/` (`q13-memo.txt`, `q13-nomemo.txt`); repro
clone KEPT at `/tmp/pp2/clone-tpch-q13` for the fix round.
