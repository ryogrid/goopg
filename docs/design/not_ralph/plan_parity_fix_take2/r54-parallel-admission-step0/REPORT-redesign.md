# R54 Redesign REPORT — re-land totals-sourcing + Q7 selectivity + split audit (2026-09-11)

Date: 2026-09-11. Scope: `REDESIGN.md` rev 2. Tree @ `493fbd104` +
uncommitted cut (re-land: `searchedtree.go` restricted accessor +
`groupingpaths.go` one assignment + 2 tests; Q7 fix: `joinselectivity.go`
OR-join estimator + 3 tests; §8). Binaries: `/tmp/pp2/bin/goopg-r54redesign`
(re-land) and `/tmp/pp2/bin/goopg-r54redesign-q7fix` (re-land + (ii));
the q7fix binary is functionally identical to the tree except a
test-only whitespace alignment made after the build. Same capped clone
and GUCs throughout (`/tmp/pp2/clone-tpch` :5534, DS Q84 on
`/tmp/pp2/clone-ds05` :5534, `work_mem=64MB`, `mpwg=4`,
`GOOPG_ANALYZE_SEED=20260905`, fresh server per phase, every query ×2
run-stable — all pairs STABLE). Top-mode (`GOOPG_PGSHAPED_DP_TRACE=1`,
`GOOPG_GATHER_PATHS=top`) + serial-mode (trace, no GATHER_TOP).
Evidence (tmp-only, NOT committed): `/tmp/pp2/r54redesign/` (drivers
`run.sh`, `run-values.sh`; per-phase server logs `start-*.log` carry
the DPPATH trace; `q{5,9,1,3,10,19}-top-{reland,q7fix}`,
`q{7,8}-{top,serial}-{reland,q7fix}`, `q84-ds05-{reland,q7fix}`,
`pp-q7fix.txt`/`pg-live.txt` parity pair; values digests). PG oracle:
live read-only PG 18.3 TPC-H on :65432 (`pg-q7-explain.txt`,
`pg-q8-explain.txt`, `q19-pg-live.plan`).

## 1. Attribution verdict (the round's exit table)

| item | reland (re-land binary) | q7fix (re-land + (ii)) | PG 18.3 | verdict |
|---|---|---|---|---|
| (i) Q5 | split 98693.70, wins by **750.20** → Finalize→Gather→Partial | byte-identical plan | splits upper | **✓ reproduces fix round to the decimal** |
| (i) Q9 | split 339368.64, wins by **29801.70** → Finalize (same shape) | byte-identical plan | splits upper | **✓ reproduces fix round to the decimal** |
| (i) Q19 | plain `Aggregate` (trace 171440.68, plan-line 166699.45; rows 9125) | plain `Aggregate`, shape-identical (trace 167641.56, plan-line 166587.05; rows 133, cost-only) | — (gate §9) | **✓ structural pin MATCH carries** |
| (i) Q1/Q84 | HashAgg 237493.63; Q84 DS-plan captured | byte-identical both | — | **✓ identical** |
| (i) Q3/Q10 | GroupAgg 234119.85 / HashAgg 107830.24 (shapes kept) | byte-identical both | — | **✓ cost-only (actually zero-move)** |
| (ii) Q7 top | split 343243.37 wins → Finalize (join rows 229626) | **gathered HashAgg 149268.71 wins by 32.82** → Gather→Parallel Hash Join **rows=1468**, groups 8817→2 | sorts, join rows **2520** | **✓ LANDS: 229626→1468 (0.58× PG), split→gathered** |
| (ii) Q7 serial | split 420835.89 wins (join 918503) | gathered 348186.42 wins by 32.82 (join 5874, groups 22) | — | **✓ measured, same 32.82 margin** |
| Q8 top | split 159409.06, wins by 161.33 | byte-identical plan | sorts | **✓ measured: dust-margin split, (iv)-clean** |
| Q8 serial | split 162929.75 | byte-identical plan | — | **✓ measured, seed justified (v)** |

(ii) gate (REDESIGN §2: fails if its margin delta is ~0 or
unattributed): top join delta is 228,158 rows (156×), attributed
end-to-end through the nation-cross clamp-off chain (§3) — **(ii)
LANDS**. Q7 sorted is NOT required of this round (REDESIGN §2); the
standing order moves split→gathered, toward PG's sort but not onto
it. Q8's continued split with a clean audit points at NEITHER
estimator NOR arm — §4 names the owner (tie-break, dust-scale).

## 2. Traced census (all margins on the lines)

Re-land reproduction (from `start-tpch-trace-top-reland.log`,
`start-q78-{top,serial}-reland.log` — every figure below is a traced
`total=` on an accepted/dominated line, ×2 stable):

- Q5: split 98693.70 accepted=WINNER (inputtotal 98693.19); gathered
  pk=0 99443.90 dominated (inputtotal 99407.21); serial hashed
  216771.70 accepted, inputtotal 216735.02 byte-identical to the fix
  round. Margin 99443.90−98693.70 = **750.20** = REPORT-fix §1 to the
  decimal. `sr.Rows=7335` confirmed live again.
- Q9: split 339368.64 accepted=WINNER (inputtotal 339156.14);
  gathered pk=0 369170.34 dominated; serial hashed 484878.39
  unchanged. Margin 369170.34−339368.64 = **29801.70** to the decimal.
  `sr.Rows=303093` confirmed live again.
- Q7 top reland: split 343243.37 accepted=WINNER (inputtotal
  338133.16, partial rows 125000); gathered pk=0 385419.94 dominated;
  sorted 910442.63; hashed 638225.58. Split wins by ~295k — the
  estimator-scale gap REDESIGN §1(iii) predicts stands until (ii).
- Q7 top q7fix: gathered pk=0 **149268.71** accepted (inputtotal
  149209.95, rows=2); split 149301.53 accepted (+32.82); sorted
  357277.16; hashed 356894.76. Partial rows 125000→**1468**.
- Q7 serial reland: split 420835.89 accepted=WINNER (partial rows
  125000); q7fix: gathered pk=0 **348186.42** accepted, split
  348219.24 (+32.82 again — same contest, same dust margin, both
  modes). Partial rows 125000→1468 in serial too.
- Q8 top (both binaries, byte-identical): split 159409.06 accepted
  (inputtotal 159403.05, partial rows 200); gathered pk=0 159570.39
  dominated (+161.33); gathered pk=1 159709.16 (+300.10); sorted
  184555.90; hashed 184417.13. The whole contest above the partial
  fits inside ~300 on 159k (0.19%).
- Q8 serial (both binaries): split 162929.75 (join rows 2370);
  hashed 201499.89. Untouched by (ii) — no OR join clause in Q8.
- Q19 plain arm: reland 171440.68 (trace-top log; plan-line
  166699.45) → q7fix 167641.56 (plan-line 166587.05): cost-only,
  shape-identical Finalize→Gather→Partial, join rows 9125→133.

## 3. Mechanism ((ii): the 0.04 provenance and its removal)

Provenance (verified in-tree before the fix): Q7's nation-cross
(n1×n2, 25×25=625) has no equijoin, so the superkey pass has nothing
to consume; the residual `[OR]` falls into
`joinClauseSelectivityExtUncached`'s `default` arm at 0.5 →
625×0.5=312.5 → M0126-0010's `max(l,r)` clamp cuts to 25. Effective
selectivity 25/625 = **0.04 — the clamp's signature, not a
measurement**, masking a 12.5× overestimate of PG's 2 (PG prices the
same single-side OR restrictions at rows=2 per nation scan).

The oracle route (read from `./postgres/`, never modified):
`clause_selectivity_ext` routes OR clauses — even join clauses —
through `clauselist_selectivity_or` (clausesel.c:810-824, via the
orclause-with-sub-RestrictInfos variant at :751). Goopg's 0.5-for-OR
was a PG divergence, not a simplification.

Fix B1 (`joinselectivity.go`, R54 (ii) comments with PG cites):
whole-OR join clauses estimate arm-by-arm — inclusion-exclusion fold
(`sel+asel−sel*asel`) over `flattenPlannerOr` arms, each arm an
independent product of conjuncts, each single-side `col=const`
conjunct priced as a restriction through `eqSelectivityForColumn`
(the restriction path's own primitive). Attribution is positional
(`SourceTableIdx`→`relInfos[].sourceIdx`, exact-one-match,
fail-closed) — never by name, because Q7's `n1.n_name`/`n2.n_name`
is name-ambiguous by construction. The guess flag is the OR over
conjunct defaults, so a partially-measured OR keeps the fallback
clamp on. SEMI/ANTI keep their own 0.5 (documented residual
divergence at `orConjunctSelectivity`). Non-estimable shapes
(inequalities, multi-relation conjuncts, unattributable columns)
keep the old 0.5 contribution — deliberately conservative vs PG's
`scalarineqsel`, each a ledgered follow-up.

Attribution chain (measured, top): nation-cross Nested Loop rows
25→**2** (PG's proper number, exact) → customer Bitmap arm
re-prices (NL rows 11990) → orders Hash Join 119904 → top Parallel
Hash Join **229626→1468** vs PG 2520 (**0.58×**, inside the 2×
harvest rule). Groups 8817→2 (top), →22 (serial). The plan gains
PG-like index-driven arms (customer `Bitmap Heap Scan`, supplier
`Bitmap Index Scan` in serial). The (ii) margin delta is 228,158
join rows through this chain — non-~0 and fully attributed.

## 4. (iv) Split per-arm audit (PROMOTED co-equal — answered with numbers)

Q8 top decomposition (the query REDESIGN §1(iii) left MEASURED):
split 159409.06 = partial 158323.05 (rows=200 partial groups) +
1086.01 Finalize/Gather-on-group-states; gathered-no-split pk=0
159570.39 = inputtotal 159552.60 + 17.79; gathered pk=1 (PG's shape
analogue: sorted+gather) 159709.16. Split wins by **161.33 (0.10%)**
over gathered pk=0 and 300.10 (0.19%) over PG's shape. No arm
over/under-prices by any material number: the partial arm, the
finalize arm, and both gather crossings are all mutually consistent
to dust. **The owner of Q8's split is the tie-break, not an arm and
not the estimator** — the contest is a near-tie PG breaks the other
way, and no tournament term moves it by more than 0.2%. Follow-up
ledgered: sorted-vs-split tie-break calibration (no constant move
scoped here; a 161-cost nudge without a mechanism would be
overfitting to one query).

Q5 confirms the audit method cuts the other way when an arm owns
it: split−gathered = 750.20, owned to the decimal by the gather
tuple term 0.1×(7335−1.2) = 733.38 + split inputtotal +19.07 (§3 of
REPORT-fix, reproduced live in §2 above).

Display-gap note (REPORT-fix §5) stands and is unchanged by this
round: plan-line upper costs on split winners sit on the plan-side
subtree basis (Q5 plan Partial 215001.42 = plan join 215001.41 +
dust vs traced 98693.70). Winners are chosen in trace-space where
the basis cancels, so the gap cannot flip an outcome; no
double-count found (Partial terms appear once per basis).

## 5. (v) PG Q8 harvest + serial judgment

PG Q8 live (:65432, pinned GUCs): `GroupAggregate rows=2405 →
Gather Merge (Workers Planned: 2) → Sort rows=1002 → Hash Join
rows=1002`. PG sorts Q8 — nothing in (i)+(ii) restores goopg's
sort, as REDESIGN §1(iii) foresaw must be named explicitly.

Seed judgment under the (v) 2× rule (totals-to-totals, the seed's
convention): goopg serial join rows **2370** vs PG totals 1002×2 =
**2004** → 1.18× ⇒ **estimator-justified** (per-worker 592 vs 1002
= 0.59×, same verdict). So serial Q8's fix-round flip was
correct-direction-on-bad-legacy, and the top split persists on a
justified seed with a dust margin. Combined with the (iv) clean
audit: Q8's gap is owned by the tie-break (§4), reached with the
estimator exonerated by its own harvest rule. No (ii)-class work
is owed by Q8.

## 6. Q19 side effect: toward/away assessment (oracle-anchored)

Shape-identical Finalize→Gather→Partial on both binaries (plan
heads byte-identical above the join line); join rows 9125→133 from
the newly-measured brand OR. Residual mechanism, stated honestly:
each OR arm's range/`IN` conjuncts (`l_quantity`, `p_size`,
`p_container ANY`) still take the 0.5 default and only the
`p_brand = const` conjuncts measure — so 133 is directionally
PG-faithful but magnitude-approximate. Anchor: PG's surviving join
rows are 47 (Nested Loop over a part Seq Scan, different join
shape, so an approximate anchor, not a ratio pin) — goopg moves
9125→133 against it: 194× over → 2.8× over, **toward and 69×
closer, not arrived**. The per-shape restriction defaults
(range/`IN` inside OR arms) are the ledgered follow-up, same class
as §3's noted conservatism. The FIX-SEED §5 must-hold (Q19 MATCH)
survives: structural pin-diff strips rows=/costs (§9), and the
shape is untouched — the plain arm prices the shared seed and shows
no seed regression (cost-only −112.40 on 166k, 0.07%).

## 7. Correctness backstop (values arm 8/8 + spotcheck substance + suites)

Full result-set digest compare, drift-controlled clone, top-mode:

| q | reland md5 | q7fix md5 | verdict |
|---|---|---|---|
| q1 | 3cfdc698… | 3cfdc698… (q7fix2) | identical |
| q3 | 1137ed9c… | 1137ed9c… (q7fix2) | identical |
| q5 | 9a6c37e9… | 9a6c37e9… (q7fix2) | identical |
| q7 | a251eec1… | a251eec1… | identical (through the new shape) |
| q8 | 6008ba10… | 6008ba10… (q7fix2) | identical |
| q9 | fef63d80… | fef63d80… (q7fix2) | identical |
| q10 | cfb5fca6… | cfb5fca6… (q7fix2) | identical |
| q19 | c408d162… | c408d162… | identical (through 9125→133) |

(q7/q19 on the q7fix binary directly; q1/q3/q5/q8/q9/q10 on the
q7fix binary via the `q7fix2` values run — plans byte-identical to
reland, digests byte-identical.) No timeouts, no hangs.

Spotcheck substance on the q7fix binary (clone, fresh server):
Q12=2 ✓ (structural invariant), Q13=34 ✓ (canonical anchor) —
the mandated pre-commit row-count check, run clone-local per the
shared-cluster collision rule rather than via `tpch-spotcheck.sh`
against the bench dir.

Suites: `go build ./...` ✓, `go vet ./internal/optimizer/` ✓,
full `go test ./internal/optimizer/` (warm cache, no `-count=1`)
✓ incl. the 5 touched tests (2 re-land + 3 (ii)). gofmt: the
touched files carry no NEW flags — one alignment in the added
passThroughs map fixed by hand; the two remaining flags
(`groupingpaths_test.go:26` const block,
`joinselectivity_test.go:521` blank line) reproduce at HEAD,
go1.25-baseline artefacts, untouched per the no-wholesale rule.

## 8. Implementation (what the cut does — for the reviewer)

Re-land (re-derived per REPORT-fix §7 + fix-round review notes, all
carried over): `searchedtree.go` new `searchedJoinInputRelOf` —
descends ONLY through row-preserving pass-throughs
(Project/Sort/Gather/GatherMerge/Memoize/OrdinalityWrap/uncapped
LockRows, depth-cap 32), nil at
Aggregate/WindowAgg/Distinct/DistinctOn/Filter/Limit/SetOp/
multi-child/unknown/nil, with the Memoize/CTEScan/Result/`ProjectSet`
scoping comments and the capped-LockRows ×2 gate;
`groupingpaths.go` `createGroupingPaths` one fail-closed assignment
(`seed.Rows = sr.Rows`, Cost untouched — input price cancels across
the live contest). Tests: descent/stop census (6 pass-throughs incl.
measured Sort(Project) chain; 12 stop cases; nil-rel/nil-node;
40-deep cap) + ordering-not-exactness seed-sizing pin.

(ii) (`joinselectivity.go`): OR dispatch in
`joinClauseSelectivityExtUncached` + `orJoinSelectivity` (fold) +
`orArmSelectivity` (product) + `orConjunctSelectivity`
(restriction pricing, positional attribution) + `relPosForSource`
(exact-one-match). Tests: `jsNationCtx` two-25-row-table fixture —
measured-arms composition vs `eqSelectivityForColumn`
(isdefault=false, below old 0.5) + guessed-conjunct marks-the-OR +
sizer-level nation-cross rows==2 (would read 25 under the old code:
the clamp-off chain pinned end-to-end).

## 9. Gate ledger (run vs deferred-with-rationale)

- FIX-SEED §5 must-holds: Q1/Q84 identical ✓ (Q84 on DS clone,
  byte-identical both binaries); Q3/Q10 shapes kept, costs recorded
  (234119.85 / 107830.24 — zero-move, inside cost-only) ✓; Q19
  structural MATCH carries (shape byte-identical above the join,
  rows=/costs stripped by the pin's structural mode — verified the
  mode, not assumed) ✓; Q5 split WINS by the 750.20 reproduction
  (a serial win would have failed the re-land; none) ✓; Q9 split
  kept (29801.70) ✓; values 8/8 ✓; suite+vet ✓; review pending
  (§10).
- `pg-plan-parity-diff` (goopg q7fix vs live PG Q19): SHAPE-DIFF
  join-method (Parallel Hash Join vs Nested Loop) — PRE-EXISTING,
  untouched by this round (goopg has always hashed Q19; PG
  nested-loops it). Not a regression; the parity tool is not the
  Q19 gate (the pin is, above).
- `estimate-audit` arm: NOT run — the (ii) gate (229626→1468 vs PG
  2520, attributed) IS the estimate audit for the touched
  estimator, measured directly against the live oracle; the full
  arm needs the shared bench cluster (collision rule) and would
  re-measure untouched estimators.
- `make plan-gate` vs committed pin: NOT run live (needs the shared
  bench server) — substituted with a file-based structural pin-diff
  reland→q7fix on all 8 queries (`rows=`/`cost=` normalised, the pin's
  structural comparison): MATCH ×7, DIFFER Q7-only, whose DIFFER is
  the intended (ii) effect (split→gathered). Q19 MATCH carries by
  shape-identity above the join line under the mode's documented
  semantics (`cmd/plan-snapshot/main.go:21-24` strips `(rows=N)` and
  ignores cost variance) — an inference from shape-identity, not a
  live pin-diff run, stated as such.
- TPC-DS SF0.5 sweep (REDESIGN (vi)): NO-GO, rationale recorded:
  the repo's own decision tree (`cmd/plan-snapshot/main.go`) says
  planner-only (selectivity) changes need plan-diff + targeted
  sweep on diverging queries — done (Q7/Q19 values 8/8 arm, Q84
  plan-identical as the DS upper-tournament representative); row
  counts are provably invariant under estimate-only changes (no
  executor touch); the fix-binary sweep already passed 95/0 on the
  same cut; the shared :65437 lane carries peer-collision risk for
  ~1h with no verdict-relevant information on offer.

## 10. Exit (owed by REDESIGN §2)

(i) RE-LANDS: Q5 +750.20, Q9 29801.70-family, Q19 shape, Q1/Q84
identical, Q3/Q10 cost-only — every figure to the decimal. The
convention-clean baseline is reproduced, not merely approximated.
(ii) LANDS: margin delta 228,158 join rows (156×), attributed
through the clamp-off chain to PG's exact nation-cross number,
0.58× PG at the top join. (iv) ANSWERED with numbers: no mispriced
arm — Q8's split is a 161-cost (0.10%) tie-break over a justified
seed. (v) ANSWERED by harvest: seed 1.18× PG totals ⇒ justified.
Q19 side effect assessed toward-PG (69× closer) with the residual
(shape-default) follow-up ledgered. Must-holds all green; nothing
moves a must-hold winner. **Recommendation: LAND the cut** (pending
the agent review below, notes to be applied).

Two fragilities recorded, neither verdict-relevant: (a) Q7's new
winner margin is 32.82 on 149k (0.02%), identical in both modes —
deterministic and ×2-stable, but a future cost tweak could flip
gathered↔split without any estimator change; (b) Q7 group count 2
vs PG 6047 — both engines guessing (PG multiplicatively), winner
unaffected in trace-space but the absolute agg costs are
estimate-driven on both sides.
