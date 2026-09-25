# M0141-S2b-12 — recon: the M0129-S1 exact-cost fuzz tiebreak masks PG's `COSTS_EQUAL` semantics

`Kind: recon` · `Parent: M0141-S2b-11`

Status: closed 2026-09-19 — recon complete; full `COSTS_EQUAL` restore
measured net-positive (agg-strategy 9→4 TPC-H, 71→44 TPC-DS; match
unchanged 7/2; Q74 healthy at SF0.25). Impl follow-up filed as
M0141-S2b-13. No production change made (recon).

## Task

`internal/optimizer/path.go:943-956` (inside `comparePathCostsFuzzily`)
returns `costsBetter1`/`costsBetter2` on the EXACT cost when total and
startup are both within `STD_FUZZ_FACTOR`, where PG's
`compare_path_costs_fuzzily` (`pathnode.c:218-220`) returns `COSTS_EQUAL`.
The deviation is deliberate (commit `ea1b2fbec`, M0129-S1: keeps a
pathkey-less hash path alive against a fuzzily-equal-cost pathkeyed merge
rival — the TPC-DS Q74 CTE self-join nested-loop-only pathology), but it
has two masking effects on the grouping election:

- at the ordered rel, a strictly-cheaper second candidate always evicts
  the first-inserted one — PG's keep-first-on-equal-dims rule never
  engages;
- at the grouped rel, the exact-cost direction makes hashed-vs-sorted
  incomparable instead of letting the pathkeys dim let Sorted dominate.

Recon scope (fix_plan): enumerate every call site/election the deviation
can affect; read the M0129-S1 rationale and motivating corpus; determine
whether a narrowing preserves the incomparability protection while
restoring `costsEqual` where all other dims are equal; evaluate the
`partialaggupper.go` hashed-before-sorted sibling; measure with the
pinned-epoch before/after `tpch-estimate-audit-arm.sh` A/B plus the
M0129-S1 motivating corpus.

## Findings (analysis)

### Call-site census

`comparePathCostsFuzzily` is called from exactly one production site:
`comparePaths` (`path.go:1034`). `comparePaths` is called from exactly one
production site: `addToPathlist` (`path.go:1223`/`:1231`). `addToPathlist`
is called only by `addPath` (`path.go:1076`) — the single funnel through
which EVERY serial `RelOptInfo.Pathlist` insertion passes (scans, joins,
grouping, ordered, window, setop). The partial-pathlist comparator
`addToPartialPathlist` (`path.go:1158`) is a separate, already-PG-faithful
implementation (the `1.0000000001` exact-cost check at `:1189` reproduces
`pathnode.c`'s tiny-fuzz rule) and is unaffected.

So the deviation's blast radius is the whole serial pathlist tournament,
but it can only matter when two candidates' total AND startup costs both
land inside the 1% fuzz band.

### The decisive site for Q4/Q12 is the grouped rel — and its signature is Q74's

`addGroupingPaths` stamps `Pathkeys` on the SORTED `PathAgg` candidate
(`groupingpaths.go:410` — the sort input's pathkeys, i.e. the group keys)
and none on the HASHED candidate (`:479-487` — keyless). The live DPPATH
trace on the post-S2b-11 binary (Q4,
`tmp/tpch-audit-m0141-s2b11-after-trace.server.log`):

- `upper.groupagg.sort` total=507569.94 `pathkeys=1` accepted (inserted
  first, per S2b-11's PG order);
- `upper.groupagg.hashed` total=506697.57 `pathkeys=0` accepted — under
  the deviation the cost dim reads weak-better-new while the pathkeys
  dim reads better-old, so `comparePaths` returns `relIncomparable` and
  both survive;
- `upper.ordered.sort` (a kind=7 `PathSort` over the hashed agg,
  `pathkeys=1`, 506697.64) then beats `upper.ordered.input` (the sorted
  agg, `pathkeys=1`, 507569.94) on the weak-cost dim with all other dims
  equal — `relADominates` — so the sorted agg is evicted and
  `DPGROUP elected shape=Sort-over-Aggregate strategy=0` (Hashed).

Under PG semantics at the grouped rel: `COSTS_EQUAL` +
`PATHKEYS_BETTER2` (old's keys beat new's) + equal outer/rows/safe →
`accept_new = false` — the hashed candidate is REJECTED before it can
seed anything downstream (`pathnode.c:541-556`). PG's ordered stage
therefore never sees a hashed rival; the sorted agg is the only offer.

Note the ordered-rel contest cannot save Sorted even under PG rules:
when both ordered-rel candidates carry equal pathkeys, PG's tie-break
chain (parallel_safe → rows → `compare_path_costs_fuzzily` at fuzz
1.0000000001 → keep-old) still removes the dearer path — the Q4 hashed
is 0.17% cheaper, which clears the tiny fuzz. The ONLY place PG's
semantics rescue the sorted candidate is the grouped-rel rejection.

### No narrowing of the deviation can produce the flip

The deviation exists to make fuzzily-tied paths INCOMPARABLE when a
non-cost dim (pathkeys) would otherwise decide — and the Q4/Q12
grouped-rel contest is the same signature (keyless-cheaper vs
keyful-dearer in-band) that M0129-S1 protected at a JOIN rel in Q74:

- demoting the weak-cost direction to `dimEqual` ONLY when it is the
  sole directional dim (the fix_plan's `costsWeaklyBetter` sketch)
  restores PG's keep-first at all-equal-dims contests, but at the
  grouped rel the pathkeys dim IS directional, so the weak direction
  stays, the paths remain incomparable, and hashed still survives and
  wins — **no flip**;
- demoting it ALWAYS is exactly `COSTS_EQUAL` — full PG semantics, and
  exactly what M0129-S1 removed.

So the choice is binary: either non-cost dims may decide fuzzy-tied
dominance (PG), or exact cost gives a directional signal (M0129-S1).
The signatures of the protected case (Q74 join rel) and the desired case
(Q4/Q12 grouped rel) are identical inside the comparator — no per-pair
rule can separate them.

### Candidate resolutions

- **A. Full `COSTS_EQUAL` restore.** Delete the exact-cost fallback.
  Correct iff M0129-S1's companion fix — the `initialRelRows` CTE
  row-estimate fallback (same commit, `ea1b2fbec`) — has moved Q74's
  hash-vs-merge costs outside the fuzz band, so the protected signature
  no longer arises. This is a MEASUREMENT question (this doc's §"Measured").
- **B. Scoped deviation.** Keep the exact-cost direction only where its
  protection is needed — e.g. non-upper rels — and restore `COSTS_EQUAL`
  at upper rels (or vice versa). `RelOptInfo` currently carries no
  upper-kind field (upper rels are identified only by their slot in
  `upperRels.rels[kind]`, `upperrel.go:95`), so this needs a marker
  plumbed to `addPath`/`comparePaths`. Still a deviation from PG, but a
  narrower one; justified only if A measurably resurrects Q74.
- **C. Sole-directional demotion (partial).** Restores keep-first at
  equal-dims contests only; cannot flip Q4/Q12. Worth noting as a
  separate small correctness fix, not this problem's fix.

Also noted: `comparePaths` lacks PG's `rows` dim and its
`parallel_safe` asymmetry inside `add_path` (pathnode.c:541-560
requires `new->rows <= old->rows` etc. before letting better pathkeys
dominate). In the cases above rows are equal on both sides, so the
outcome is the same, but it is a residual simplification of the same
function — recorded for the follow-up task to consider.

## Measured

Experiment: throwaway worktree `/tmp/s2b12-wt` at `59313518e` with the
exact-cost fallback deleted (double-fuzz → `costsEqual`, i.e. candidate
resolution **A**, full PG restore). Binary `bin/goopg-s2b12-fullrestore`,
never committed. Baselines: same-day S2b-11-era captures.

### TPC-H (pinned epoch `GOOPG_ANALYZE_SEED=20260905`, `PGSHAPED=1`, ref `:65432`)

8/22 plans changed. Per `pg-plan-parity-diff.py` vs the same-arm PG
capture (`analysis/m0141/m0141-s2b12-fullrestore/`):

- match: **7 → 7** (unchanged — flipped queries still diverge elsewhere)
- `CATEGORIES-EXCL-MATCH` deltas:
  `aggregation-strategy` **9 → 4**, `parameterisation` 5 → 3,
  `join-order` 13 → 15, `join-method` 9 → 10, `scan-type` 8 → 9
  (net 55 → 52 tagged queries)
- **Q4 and Q12 flip `HashAggregate` → `GroupAggregate`** — the expected
  S2b-11 movement, now unmasked. Q5/Q8/Q21/Q22 also move to PG's
  aggregation election; Q8's join tree converges too. Q9 moves AWAY
  (PG elects `HashAggregate` on a real >1% margin there; goopg's own
  cost model produces a fuzzy tie, so the restored tie-break resolves
  toward the sorted agg — the comparator now exposes a pre-existing
  cost-model disagreement that exact-cost direction was hiding).

### TPC-DS SF0.25 (private clone `:5591`, patched binary vs baseline capture)

- Plan-shape census vs same-day baseline
  (`plans-20260918-195631.txt`): **75/99 changed** — dominated by
  systematic `HashAggregate → GroupAggregate+Sort` flips at fuzzily-tied
  grouped rels, plus join-pathlist re-elections (merge-vs-hash) and
  partial-agg arm changes.
- Parity vs PG `tpcds025` (`:65438`, live EXPLAIN capture, read-only):
  match **2 → 2**; `aggregation-strategy` **71 → 44**,
  `sort-strategy` 77 → 71, `join-order` 91 → 90, `join-method` 69 → 68,
  `scan-type` 59 → 58; `parameterisation` 45 → 46,
  `parallelism` 84 → 86, `qual-placement` 20 → 22, `rendering` 23 → 24
  (net 539 → 509).
- **Q74 stays healthy**: Hash Joins throughout the CTE self-join chain,
  Parallel Hash Join inside the CTE — no nested-loop resurrection.

### Why Q74 does not resurrect

`ea1b2fbec` carried TWO fixes. Fix #2 — the `initialRelRows` CTE
row-estimate fallback (`0.005^4 * 17977 ≈ 0` floored to 1 had made
nested loops look free; using the CTE body's row count restored sane
estimates) — is what structurally removed the NL pathology. Fix #1 (the
comparator deviation) was belt-and-suspenders keeping a hash path alive
inside the fuzzy tie. With fix #2 in place the NL candidate is never
cost-competitive at any scale, so `COSTS_EQUAL` cannot resurrect the
original bug — the residual risk is only a *sane-but-slower* merge-over-hash
election where the fuzzy tie still occurs at other scales (M0129-S1 was
measured at SF0.5; the clone was SF0.25).

## Conclusion and recommendation

The deviation cannot be *narrowed* into the desired behavior — the
grouped-rel signature it must release (keyless-cheaper vs keyful-dearer)
is identical to the join-rel signature it was added to protect. The only
PG-faithful resolution is **A: full `COSTS_EQUAL` restore**, justified
empirically: the motivating pathology is held down by the companion
estimate fix, and the restore is net-positive on parity instruments
(agg-strategy 9→4 TPC-H, 71→44 TPC-DS) though match counts don't move.

Blast radius is large (75/99 TPC-DS) and includes join-pathlist
re-elections plus at least one away-from-PG case (TPC-H Q9) driven by a
pre-existing cost-model disagreement the tie-break now exposes. The impl
task must therefore re-verify Q74 (plan + timing, ideally at SF=1/SF0.5),
run the full row-count sweep, and update `path_test.go`
(`TestComparePathCostsFuzzily_WithinFuzzIsEqual` pins the deviation).
Sibling site for the same task: `partialaggupper.go` no-split arm inserts
HASHED before SORTED (`:399` vs `:410`) — the same inversion class S2b-11
fixed in `groupingpaths.go`, only reachable once `COSTS_EQUAL` makes
insertion order meaningful.

Filed as **M0141-S2b-13** (Kind: impl).
