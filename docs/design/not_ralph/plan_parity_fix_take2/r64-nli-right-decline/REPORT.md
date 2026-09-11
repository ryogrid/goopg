# R64 REPORT — decline NLI when the probe is the preserved side (2026-09-11)

SCOPE: `r64-nli-right-decline/SCOPE.md` (reviewed APPROVE-WITH-NOTES —
stale line citations corrected, fail-closed panic extended to all three
consumers per note 2, addNLIPaths-only guardrail stated per note 3; no
re-measurement — the notes were doc/code-precision, not mechanism).
Consumes ledger row R63-#3 (Memoize+RightJoin wrong results).

## 1. Result: Q13 33→34, the corpus's sole NLI-Right gone, everything else bit-identical

The cut is ONE gate + two fail-closed panics + one unit pin, in three files:

- `internal/optimizer/joinpathsnli.go` (`addNLIPaths`): `if jt ==
  parser.JoinRight { return }` — the admitted set mirrors
  `partialHashJoinTypeOK` (Inner/Left/Semi/Anti); FULL never reaches here.
- `internal/optimizer/createplannl.go`: panic on `JoinTypeRight` in the
  index NLI arm (covers fused + decomposed lateral `Join` — the lateral
  stream has no `fillInner`, so a `Join{Lateral,Right}` would silently
  degrade to Inner) and in the bitmap NLI arm. A Right over a
  parameterized inner is always wrong: loud failure, not dropped rows.
- `internal/optimizer/joinpathsnli_test.go`:
  `TestNLIArmDeclinesPreservedInner` (commuted-Right emits zero
  parameterized `PathNestLoop`; forward-Left still admits).

Headline numbers (bin `goopg-r64`, fresh `cp -a` clones
`/tmp/pp2/clone-tpch-r64` :5541 vs `goopg-r63` on
`/tmp/pp2/clone-tpch-r64base` :5542, identical bench data, seed 20260905):

| Query | Before (R63 tree) | After (fix) |
|---|---|---|
| Q13 rows | 33 | **34** (`0\|50000` restored, custdist SUM=150000) |
| Q13 plan | Nested Loop Right Join + Memoize probe | **Hash Right Join** (PG's shape) |
| All other 21 TPC-H | — | md5 MATCH (ordered+unordered+colsig) |
| Q11 pins | 32000 / 10666 | 32000 / 10666 (bit-identical) |
| Q12 | 2 | 2 |

Mechanism chain (SCOPE §1, now resolved): the commuted-LEFT direction
(outer=orders, inner=customer probe, jt=Right) no longer offers an NLI
path, so createPlan never builds the fused `NestedLoopIndexJoin{Right,
InnerMemo}`; the direction's hash path wins → Hash Right Join, byte-shape
PG's own Q13 plan. The defect was the DIRECTION, not the cache (§1.5):
a bare-probe NLI-Right would drop the same rows.

## 2. Gate ledger

1. `go test ./internal/optimizer/` green (2.5s, no `-count=1`, new pin
   included); `go vet` clean.
2. **Values**: `tpch-runner -digest` full sweep both lanes + `-diff`: **23
   MATCH, 1 ROWS-DIFF (Q13 33→34)** — the fix delta, nothing else.
   (`-diff` exits 1 on any non-match; the sole diff is the fix.)
3. **Plans**: `tpch-runner -explain` both lanes, cost/rows/width/elapsed
   normalised: diff = **exactly the Q13 join episode**
   (NL-Right+Memoize → Hash-Right + Sort); all other 21 queries
   bit-identical. Corpus-wide grep: the sole `Nested Loop Right Join`
   (Q13:279) is gone; the only surviving `Right Join` is Q13's `Hash
   Right Join`. P1's "surviving NLI-Right → STOP" guard passes.
4. **pp**: covered by gates 2–3 (exactly-one-move, toward-oracle —
   stronger than verdict comparison for a one-query shape fix).
5. **DS SF0.5 sweep**: LANDS — `SUMMARY: PASS=94 (57 ck-verified, 37
   ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4` (private
   lane: fresh `cp -a` clone `/tmp/pp2/clone-ds05-r64` :5543, tmp-only
   `/tmp/pp2/sweep-r64.sh`, bin `/tmp/pp2/bin/goopg-r64`, reports
   `/tmp/pp2/r64/ds05/sweep-20260911-164548.txt`). TIMEOUT is Q72 alone
   (307s, oracle 100 rows); SKIP=4 are oracle-side (Q4 oracle-TIMEOUT,
   Q36/Q70/Q86 SKIP_QUERYGEN) — the R62 baseline set exactly, no new
   mismatch/ckmismatch/error, no flip to adjudicate. No SWEEP VOID
   line: engine-id stable across the run.
6. REPORT.md (this file); review, then commit + push.

## 3. Deviations from SCOPE (ledgered, none blocking)

1. Review note 1: three stale line citations corrected (`Jointype: jt`
   :327, partial V1 gate :390-393, tryBuildNLI decline :324-327).
2. Review note 2 ADOPTED: the fail-closed panic covers the decomposed
   lateral arm too (it shares the `jtNLI` computation — one panic site
   covers fused+decomposed, plus one in the bitmap arm).
3. Review note 3 ADOPTED: the addNLIPaths-only guardrail is stated in
   SCOPE §2 (plain-NL Right stays legal via the generic sweep).

## 4. Sequencing (user order, preserved)

After this round lands: re-verify R63 gates post-fix → R63 commit → push
→ SF0.25 migration (cluster rebuild, golden replacement, AGENT.md
updates; past analysis reports untouched).

Evidence tmp-only `/tmp/pp2/` (`r64-dig.txt`, `r64base-dig.txt`,
`r64-plans.txt`, `r64base-plans.txt` + `.norm`, `r64-start.log`,
`sweep-r64.sh`, `r64/ds05/` pending); clones `clone-tpch-r64` :5541,
`clone-tpch-r64base` :5542 (lanes DOWN, evidence saved),
`clone-ds05-r64` :5543 (sweep lane); binaries `/tmp/pp2/bin/goopg-r64`
(+`goopg-r63` pre-fix A/B).
