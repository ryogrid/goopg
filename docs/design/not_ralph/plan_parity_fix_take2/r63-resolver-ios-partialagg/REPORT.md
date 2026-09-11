# R63 REPORT — resolver arms: IndexOnlyScan + partial-agg group-key identity (2026-09-11)

SCOPE: `r63-resolver-ios-partialagg/SCOPE.md` (reviewed
APPROVE-WITH-NOTES — 1 blocking + 5 notes applied, no re-measurement;
commit `f3188003f`). R62 LANDED (`163fecd3c`): Q11 search-exact 32000 /
display 10666, pp 5/15/0/2, DS PASS=94 + Q72 TIMEOUT.

## 1. Result: gates 1–4 PASS — M1 display 200→search-exact, everything else bit-identical

The cut is TWO arms + one test amendment in two files (no Yao formula,
clamp, consumer, display-code, or executor change):

- `internal/optimizer/joinkeyproof.go`: `*IndexOnlyScan` arm in
  `resolveBaseColumn` (output `idx` → `Covered[idx].Name` → position in
  `tbl.Columns` → `baseColumnOfTable(x, x.Table, nil, tableIdx)`; IOS
  carries no UniqueKeys; empty/mismatched `Covered` misses exactly as
  today) + partial-mode-ONLY `*Aggregate` arm (bare `*ColumnRef`
  `GroupExprs[idx]` recurses into `x.Child` with the remapped index —
  the *Project rule one level down; anything else misses exactly as
  today).
- `internal/optimizer/joinkeyproof_arms_test.go`: `*Aggregate` exemption
  with reason (groups, not base-rel rows — the Yao walk must not
  descend). NO twin-walker edit: IOS is a leaf (no `Child` field),
  reached via the `n == rel` identity check exactly like
  `*SeqScan`/`*IndexScan` (review blocking note — the draft's
  `passthrough(x.Child)` would not compile).
- `internal/optimizer/cardinality_groupunique_test.go` +
  `cardinality.go` doc comment: the failure-driven amendment (§2).

Headline numbers (bin `goopg-r63`, fresh `cp -a` clone
`/tmp/pp2/clone-tpch-r63` :5533, seed 20260905):

| Query | Before (R62) | After (R63) | PG oracle |
|---|---|---|---|
| M1 display Partial/Finalize/Gather rows | 200 | **196849** (search-exact, §2) | 203361 (~3% stats gap, #6(ii)) |
| Q11 HashAggregate / Sort-above | 10666 | 10666 (bit-identical) | 10667 |
| Q5 HashAggregate / Sort-above | 25 | 25 (bit-identical) | 25 — immobile ✓ |
| Q11 seed join rows | 32000 | 32000 (bit-identical) | 32000 |

Mechanism chain (exactly SCOPE §1, now resolved): `examineGroupVar`
nd=196849 stands through the IOS arm (Partial's child) and the
partial-agg arm (Finalize's Gather→Partial chain) → Yao term sees
`filtered=tuples=800000` (no OuterColumnRef — R62 arm cannot fire) → no
discount → clamp min(196849, 196849) = **196849**. Zero `fallback200`
lines survive under R63 (was 9× IOS-direct + 1× Gather-child).

## 2. Gate ledger

1. `go test ./internal/optimizer/` green (2.6s, no `-count=1`,
   arms-agreement included); `go vet` clean.
2. **Values**: all 22 TPC-H queries md5 MATCH vs the R62 binary on
   IDENTICAL data (same-data A/B — stronger than the 8+q11 gate);
   Q11 pinned 32000/10666; Q5 pinned 25.
3. **DPTRACE A/B** (`dp-a.txt` vs `dp-b.txt`, cost-stripped): **3
   modified lines, nothing else** — display-episode
   `upper.partialgroupagg.partial` + `upper.groupagg.finalize` +
   `upper.groupagg.gathered` rows 200→196849; search lines (incl. all
   costs) bit-identical; no joins (single-table).
4. **pp**: per-query verdicts R62-binary-vs-R63-binary on identical
   data+oracle IDENTICAL (1/20/1/0/0 both — the R62 REPORT's 5/15/0/2
   gap is a lane artefact, §4 note 1); `shape-delta` text-changed=0
   shape-changed=0 across all 22 TPC-H plans (pp normalises estimates
   out; zero movement is the strongest P3 outcome).
5. **DS SF0.5 sweep**: PENDING (lane ready: fresh `cp -a` clone
   `/tmp/pp2/clone-ds05-r63`, patched tmp-only `sweep-r63.sh`,
   criteria PASS=94 same set + Q72 TIMEOUT alone, plan set
   re-adjudicated per P3). ON HOLD — the pre-commit spotcheck below
   fails on a pre-existing bug, so the commit (and therefore this
   sweep) is blocked until §7 is resolved.
6. REPORT.md (this file); review, then commit + push — BLOCKED by §7.

## 7. Pre-commit spotcheck FAIL — root-caused to a pre-existing
Memoize + RIGHT JOIN wrong-results bug (R63 exonerated, commit BLOCKED)

`scripts/tpch-spotcheck.sh` (practice-card must-gate): Q12=2 PASS,
**Q13=33 rows vs expected 34 — FAIL**. Bisect on a private clone
(`/tmp/pp2/clone-tpch-q13`, `cp -a` of the bench data dir, :5538):

- R63 binary: 33 rows; R62 binary on IDENTICAL data: 33 rows with
  **byte-identical digests** (`colsig`/`ordered`/`unordered` all match)
  and a byte-identical plan (costs included). R63 moves nothing —
  zero row-count movement, consistent with gates 2–4.
- `GOOPG_MEMOIZE=off` on the same data: **34 rows**, plan flips to
  `Hash Right Join`, full output checks out (`0|50000` present,
  custdist SUM=150000, and the `0|50000` cell independently verified
  by a join-free `NOT EXISTS` count = 50000).
- Memoize plan output: 33 rows, SUM=100000 — exactly the `0|50000`
  group (all 50,000 zero-order customers) vanishes. Base tables are a
  clean SF=1 load (customer=150000, orders=1500000).
- r59 binary already picks the identical Memoize plan (same costs):
  the flip AND the bug both predate R59. Timeline: 08-26 pin at 34
  (commit `75266ebee`), then take2 P2-02c (`62a5006c7`, 09-03,
  `enable_memoize` per-session) flipped Q13 onto the Memoize path and
  exposed the latent executor bug — that round's gate should have
  caught 34→33.

Mechanism class (for the fix round): Memoize above the inner side of
a RIGHT JOIN (planned from customer's LEFT JOIN) drops the
null-extended rows for unmatched inner rows — PG's Memoize/NL-right
bookkeeping for "which inner tuples matched" does not survive the
cache. PG 18 itself plans Q13 as `Hash Right Join` (verified on :65432).

Evidence tmp-only: `/tmp/pp2/q13-memo.txt` (33 rows, SUM=100000),
`/tmp/pp2/q13-nomemo.txt` (34 rows, SUM=150000); clone kept at
`/tmp/pp2/clone-tpch-q13` for the fix round. Ledger row: R63-#3
(Memoize+RightJoin drops null-extended inner rows).

## 3. Failure-driven amendment (owned, not drift)

`TestGroupUniqueNDistinctRefusesAPartialAggregate` failed after the cut
(ndistinct through a Partial = 700, want 0) — the cut's first live
contact with a pinned refusal, and the pin was RIGHT about uniqueness
but wrong about the question `columnNDistinctForChild` asks. Resolution,
both halves now pinned in the test:

- `groupUniqueNDistinct` STILL refuses partials (called directly in the
  test): PG's isunique branch needs each value once; two workers can
  emit the same key. Unchanged, exactly as SCOPE §2 designed.
- `columnNDistinctForChild` through a partial now answers base nd (700)
  via the R63 arm: distinct keys present across partial output = groups
  observed = group count in the high-nd case (M1's display needs exactly
  this); skewed workers over-count bounded by base nd (R63-#1).
- The `columnNDistinctForChild` doc comment's "no arm that walks through
  a grouping node … deliberately" clause updated to the partial-only
  form (it now names R63 and the shadowing reason).

No re-audit was triggered beyond the DPTRACE A/B already in gate 3:
the failure is IN the predicted blast radius (SCOPE §4), the mechanism
is fully chained, and the A/B shows only the three display lines move.

## 4. Deviations from SCOPE (ledgered, none blocking)

1. **Precision note (P1 literal)**: SCOPE predicted display →201356
   (the probe lane's search number). Gate lane search is **196849** —
   both fresh `cp -a` clones agree deterministically (digests MATCH
   across clones), so the probe lane's 201356 is that older clone's
   carried stats (~2.7% nd sampling/seed-generation gap, #6(ii)
   family — same class as R62's Q39 note). P1's substance LANDS
   (display == search exactly, fallback200 extinct); the literal is
   lane-stats, recorded not re-audited. Same-load proof: R62-file
   values "mismatches" dissolve on inspection (row counts identical —
   the R62 files carry 2 `SET` header lines the digest script strips).
2. **P3 over-delivers**: "expect SMALL" movement → ZERO text movement
   on TPC-H (no TPC-H plan on this load routes a group-var through an
   IOS leaf or partial-split; the Q1/Q6 WATCH does not fire). DS
   movement adjudicated in gate 5.
3. **Twin-walker correction** (review blocking): no `relFilteredRowsWalk`
   edit — IOS is a leaf; plus the `Cond`-residual soundness correction
   (`EstimateRows(IOS)` prices keys, not `Cond` — conservative,
   M1 Cond-free). Both in SCOPE as reviewed.
4. **R63-#2 recorded** (review-found): `soleBaseScan`,
   `IsSmallDimensionSide`, `outerScanRowCount` lack IOS arms —
   conservative, M1-unaffected, unpinned; follow-up, not this round.

## 5. Re-audit accounting

P0 LANDS (zero fallback200 under R63), P1 LANDS modulo the §4.1 lane-stats
note (196849 == search exactly), P2 LANDS ×22 (sovereign same-data A/B
plus Q11/Q5 pins), P3 LANDS in strongest form for TPC-H (verdicts
identical, shape-delta 0/0; DS pending gate 5), P4 LANDS (cost-stripped
diff = exactly the 3 display group-count lines). No P-miss stands.

## 6. Ledgered follow-ups (unchanged + 1 addition)

- R63-#1 (partial-skew over-count, bounded by base nd), R63-#2 (IOS-less
  sole-scan/small-side/outer-count arms), rounding n/a, (a) Materialize
  (insufficient-alone), #6 (cost-model/stats, incl. the 201356/196849
  lane-stats gap and M1's 196849-vs-203361 oracle gap), R61 #4
  (fail-closed-correct), (b) Q4 (3× deferred), NLI staleness comment,
  R61 #5 (executor watch), R62-#1 (no live case).

Evidence tmp-only `/tmp/pp2/r63/` (tops/values/pp/dp-A-B/trace logs,
`sweep-r63.sh`, ds05/ pending); clones `/tmp/pp2/clone-tpch-r63` :5533,
`/tmp/pp2/clone-tpch-r63t` :5534 (R62 A/B binary `goopg-r62`),
`/tmp/pp2/clone-ds05-r63` (gate-5 lane); binaries `/tmp/pp2/bin/goopg-r63`
(+`goopg-r62` A/B). PG references :65432 (M1 203361) with
`PGPASSWORD=tpch`.

## 8. Post-fix closure (2026-09-11) — standalone gate 5 superseded, not run

The §2.5 standalone DS SF0.5 sweep on the R63-only tree never ran: the
§7 spotcheck FAIL blocked the R63 commit, and per the user-sequenced
order the R64 fix landed on top first. R63's gates are instead
re-verified post-fix on the combined tree — TPC-H values A/B
(23 MATCH + Q13 33→34 fix delta, Q11/Q5 pins hold) and the DS SF0.5
sweep (PASS=94 same set + Q72 TIMEOUT alone, no flip to adjudicate)
are recorded in the R64 REPORT's gate ledger. R63's cut is
estimate-only with zero TPC-H value/plan movement (gates 2–4), so no
DS movement can be attributed to it outside R64's adjudication.
