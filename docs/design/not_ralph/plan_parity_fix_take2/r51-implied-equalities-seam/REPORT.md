# R51 — implied-equality seam switch (report, 2026-09-10)

*Plan: `SLICE.md`. Design basis: `K26-*.md` §§7–9.*

## 0. Change (ONE line + 2 justified re-baselines)

`joinsearchseam.go:458`: `inferEquivClassConstants` →
`inferTransitiveEqualities` (same `inferEqualitiesClosure`, flag
false→true) + deferral comment rewritten (R51 rationale, K26 §§7–9
chain, costing-half-open honesty). Test re-baselines in
`pathtarget_test.go` (see §2).

## 1. Unit blast radius: exactly K26's prediction, then zero

With the flip on, full `internal/optimizer` suite: only the 2 predicted
Slice3 keep-assertions fail; `TestPreDPPinnedSemiKeysResolveAfterDP`
PASSES (K26 §9.1's kept `nliProbeKeys` fix holds on the current tree).
After re-baseline: optimizer + executor suites green, `go vet` clean
(no `-count=1`).

## 2. Adjudication (R14 precedent — PG decides, not the old pin)

- **`TestSlice3LiveQ9ShapeDerivation`.** Old pin: witness build 10→7.
  New actual: 10→8 (keeps `s_suppkey` — the new order reads it above
  the narrow point; same rule the test comment states). JUSTIFIED:
  TPC-H Q9's new plan reaches PG's innermost join `partsupp ⋈ part`
  on `ps_partkey = p_partkey` — the synthesised clause K26 §2 names
  (`{part}|{partsupp}` enumerated AND chosen). Q9's headline drops
  `join-method`. Re-baselined want-set + comment.
- **`TestSlice3SelfJoinInDerivedTable`.** The F4 pair-rule loop passes
  UNCHANGED (no "keeps 1 of 2" errors) — the invariant holds under the
  new order. Only the existence pin failed: the (nation a ⋈ nation b)
  build now keeps full symmetric 3-column copies
  (`[n_nationkey n_name n_regionkey] ×2`) instead of 2-column pairs.
  JUSTIFIED: order moved via the synthesised `a.n_nationkey =
  r.r_regionkey`; pin updated to the new actual, F4 loop untouched.

## 3. Corpus A/B (clone discipline, pinned GUCs, inode-verified binary
`/tmp/pp2/bin/goopg-r51`; PG arms reused from the METHODOLOGY2 recount —
PG side unchanged)

- **TPC-H:** verdicts 2/20 unmoved. Categories: join-method 11→**9**
  (−2: Q5 + Q9 both drop the tag), aggregation-strategy 8→9 (+1: Q5
  sideways), **join-order 18→18** — K26 §9.2 confirmed: the candidate
  half is now open but the DP still does not CHOOSE PG's order
  (costing half). Shape-delta: 3 text (Q5, Q9 + Q15a splice-header
  artifact — plan body identical, see §5), 2 shape (Q5, Q9).
- **TPC-DS:** verdicts 1/71 unmoved (missing-node 24, error 3).
  Categories: scan 72→73, sort 80→81, parallelism 89→90,
  qual-placement 9→10 (+1 each — headline-tag side effects, none a
  regression); join-order 95→95, join-method/param/agg/rendering flat.
  Shape-delta: 21 text-changed, 7 shape-changed
  (Q4 Q11 Q25 Q31 Q64 Q72 Q84).

## 4. Values gates (all green)

- TPC-H canonical digest **24/24 MATCH** (`tpch-runner-r51 --diff`
  vs `bench/tpch/baseline-digests.txt`, `/tmp/pp2/dig-r51.txt`;
  fresh capped GOGC=100 server on `/tmp/pp2/clone-tpch :5534`).
  Spotcheck Q12=2/Q13=34 canonical.
- TPC-DS SF0.5 sweep all-zero: **PASS=95 MISMATCH=0 CKMISMATCH=0
  ERROR=0** (foreground; report
  `/tmp/pp2/sf05-r51-results/sweep-20260910-182942.txt`).

## 5. Provenance / traps

- Q15a splice repeated from the METHODOLOGY2 recount (raw `q15a.sql`
  still lacks its EXPLAIN prefix — working-copy fix still open);
  the Q15a "text-changed" line is the splice header padding, not a
  plan delta (diff = header line + trailing blank only).
- DS diff needed the normalised PG arm (`rec-pg-ds.norm` — raw
  `===== Qn =====` markers diff as `error=99 missing PG section`;
  K90-family signature, caught by inspection).
- Servers `:5533`/`:5534` stopped post-gates via `goopg stop -D`.

## 6. What remains (explicit, not deferred silently)

- **join-order costing half** (K26 §9.2): the DP enumerates PG's pairs
  now (Q9 proves it) but prices other orders. That is the next round —
  `join_search_one_level` costing work, root-causes' original territory.
- Q5's +1 aggregation-strategy tag and the 4 DS +1s are unnamed at the
  query level (same standing requirement as METHODOLOGY2's sideways
  moves).

## 7. Review disposition (2026-09-10, agent review of the R51 diff)

Verdict: **APPROVE-WITH-NOTES** (no code changes requested). Verified:
(a) C-04a ordering TRUE — the closure's inputs are purged of
nullable-side members before it runs (`heldAbovePrefix` holds WHERE
conjuncts off admitted links' nullable sides; inner-`ON` overlap
declines first; outer-`ON` quals join only after the closure), so no
transitive `a = c` can reorder across a link; (b) both re-baselines
SOUND under R14 (Q9's new innermost join is PG's pair — progress, not
parity; F4 pair-rule loop passes unchanged); (c) executor/value risk
LOW — synth clauses are logically implied equalities evaluated by the
executor, worst case redundant evaluation, and values binding held
(24/24 + all-zero sweep + green suites). Carried forward, not dropped:

1. Before the costing-half round, name each of the 7 TPC-DS
   shape-changed queries (Q4 Q11 Q25 Q31 Q64 Q72 Q84, §3) and Q5's
   agg-sideways tag against PG — unadjudicated shape movement must
   not accumulate.
2. The §5 provenance notes (Q15a splice-header artifact; DS
   `rec-pg-ds.norm` arm) stay attached to the §3 corpus numbers so a
   future recount does not re-litigate them.
3. Optional hardening: assert in a test that the closure input
   contains no nullable-side relids (today the guarantee lives only
   in code comments). Pre-existing alias note: synth nodes reuse the
   original `*ColumnRef` pointers — read-only today, same as the
   constants half; no new hazard from the flip.
