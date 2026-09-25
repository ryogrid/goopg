# R62 REPORT — Yao through parameterized probe: decline per-scan evidence (2026-09-11)

SCOPE: `r62-yao-parameterized-probe/SCOPE.md`. R61 LANDED
(`r61-groupcount-search-input/REPORT.md`, commit `09888fe69`): Q11 groups 80
(input fix), display 26 (80×1/3); PG wants 32000 → 10667.

## 1. Result: all six gates PASS — Q11 is PG-exact at search, ±1 at display

The cut is ONE arm + one helper in a single file (no Yao formula, clamp,
consumer, display-code, or executor change):

- `internal/optimizer/cardinality.go` (+24/−0): new `indexProbeHasOuterRef`
  helper (reuses the sibling same-scope detector `exprHasOuterRef` /
  `exprHasOuterRefList`, `narrowoutput.go:506/525` — subplan interiors stepped
  over as their own scope; unenumerated key shapes decline, i.e. decline the
  evidence, the PG-faithful side); one arm in `relFilteredRowsWalk`'s
  `n == rel`: a parameterized probe returns `(0, false, false)`, skipping the
  Yao term exactly in that case.

Headline numbers (bin `goopg-r62` = `a252a3ab…`, clone `:5533`, seed 20260905;
fresh rebuild md5-identical, provenance confirmed):

| Query | Before (R61) | After (R62) | PG oracle |
|---|---|---|---|
| Q11 search `grouped.Rows` (both aggs) | 80 | **32000** | 32000 — EXACT |
| Q11 HashAggregate / Sort-above | rows=26 | **rows=10666** | 10667 (±1 truncation, ledgered) |
| Q5 HashAggregate / Sort-above | rows=25 | rows=25 (plans bit-identical) | 25 — immobile ✓ |
| Q11 seed join rows | 32000 | 32000 (bit-identical) | 32000 |

Mechanism chain (exactly SCOPE §1.ii, now without the discount):
`examineGroupVar` nd=201356 stands (no per-probe `filtered=80` evidence) →
Yao term skipped → closing clamp min(201356, 32000) = **32000** →
display 32000×(1/3) = 10666.67 → **10666 by truncation** (R61 precedent
26.67→26; `scaleByFloat` truncates). The ±1 vs PG's 10667 is
truncation-vs-round, cosmetic, own scope (ledgered, not gated).

## 2. Gate ledger

1. `go test ./internal/optimizer/` green (2.490s, no `-count=1`); `go vet`
   clean.
2. **Values**: q1/q3/q5/q7/q8/q9/q10/q19 md5 MATCH vs `/tmp/pp2/r61/` (8/8);
   q11 values MATCH vs R61 (md5 `b8337742…`, both). Q5 groups pinned at 25
   (top plans bit-identical, rows=25 present both aggs).
3. **DPTRACE A/B** (`dp-r61.txt` vs `dp-r62.txt`): **3 modified lines, nothing
   else** — `upper.groupagg.hashed` + `upper.groupagg.sort` rows 80→32000
   (total 7470.09→7869.09 / 9944.61→10343.61), `upper.ordered.sort` rows
   26→10666; **all join traces bit-identical**, all verdicts
   accepted→accepted.
4. **pp**: `match=5 shapediff=15 unparsed=0 missingnode=2` — R61's 5/15/0/2
   exactly. Per-query verdict diff vs R61: ONLY Q11 (numbers 26→10666, same
   categories) and Q3 move. Q4/Q5/Q10 verdicts identical. TPC-H Q3
   HashAggregate→GroupAggregate+Sort: rows 12025→**307640** vs PG **308817**
   (strongly toward-oracle); strategy away from PG's HashAggregate — new
   cost-model ledger item §6 (#6: hash costing at large group counts).
5. **DS SF0.5 sweep** (foreground, fresh rebuild md5-identical to the A/B
   binary; private lane: fresh `cp -a` clone `/tmp/pp2/clone-ds05-r62` :5535
   via tmp-only script copy, bench cluster never touched):
   `sweep-20260911-123234.txt` — **PASS=94 (57 ck-verified, 37 ck=n/a)
   MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 (Q72 alone, 300s vs R61 325s;
   TIMEOUT readings excluded from deltas) SKIP=4** (Q4 oracle-TIMEOUT +
   36/70/86 querygen). Status-delta vs R61: **verdict-changes=none,
   runtime-moves=0** (no ≥2.0x move at 5s floor), total 1242s→1227s (−1.2%;
   manual cross-round sum of per-query readings from both sweep files — the
   R62 sweep file itself records "no baseline", so this delta is not
   derivable from it alone; SUMMARY-identity and Q72-alone are file-verified).

## 3. DS plan-shape channel: 19 changed adjudicated (non-blocking, P4 holds)

Plan-diff vs R61 (`scripts/tpcds-plan-diff.py`): same=80, changed=19 —
**15 pure number-moves** (agg rows 1→N propagation + cost terms; the predicted
blast radius — e.g. DS-Q3 HashAggregate 15→**41**, the SCOPE WATCH confirmed:
its group rel was probe-restricted) and **4 cost-driven strategy flips**,
all values-PASS, all toward-oracle in the hash-vs-sort dimension:

| Q | Move | vs PG oracle (`bench/tpcds/plans-pg`) |
|---|---|---|
| Q15 | HashAgg → GroupAgg+Sort (rows 1→144) | PG uses GroupAggregate — TOWARD |
| Q45 | HashAgg → GroupAgg+Sort (rows 1→56) | PG uses Finalize/Partial GroupAggregate — TOWARD (strategy; PG additionally parallel; mirrors R61-Q40) |
| Q37/Q82 | HashAgg → GroupAgg+Sort (rows 1→40 / 1→80) | PG uses `Group` over sorted (Gather Merge) input — TOWARD in hash-vs-sort dimension (PG additionally parallel, plain Group vs GroupAgg) |
| Q39 | numbers-only: body agg 48→3202, CTE-scan propagation | PG body agg 7823 — toward (magnitude gap is column-stats scope, see §4) |

Mechanism for all 19: reldistinct now survives undiscounted, repricing
agg-feeding subtrees and flipping strategy comparisons the old rows=1/80
priced wrong. No new TIMEOUT, no CKMISMATCH — the planner working as designed.

## 4. Deviations from SCOPE (ledgered, none blocking)

1. **Precision note (P4 magnitude)**: Q39 body agg predicted "~7823",
   actual **3202**. Mechanism fully accounts for it: with no discount,
   reldistinct passes through at goopg's own nd (no clamp binds: 3202 < 9607
   input). The residual 3202-vs-7823 is a column-`n_distinct` stats gap, not
   an estimator-mechanism gap — grouped under new ledger item #6. Direction
   (48→3202) and numbers-only shape both LAND.
2. **Precision note (P4 flips)**: "no NEW shape flip vs R61's 7" read
   strictly misses the 4 DS flips + TPC-H Q3 — but every one is
   cost-driven-from-corrected-counts, values-PASS, and toward-oracle per the
   table in §3 (same adjudication class as R61's own 7). Documented, not a
   mechanism miss: the decline arm fired on every probe site (DP shows no
   surviving discount), and no flip contradicts an oracle strategy without
   the §6 (#6) cost-model framing.
3. **Lateral**: DS-Q3 WATCH converts to VERIFIED (15→41 lift iff
   probe-restricted — it was).

## 5. Re-audit accounting

P0 fired (all 8 Yao calls skip — DP shows both grouped rels at undiscounted
rows), P1 LANDS (search EXACTLY 32000), P2 LANDS (display exactly the
predicted truncation 10666), P3 LANDS (values all MATCH, Q5 immobile at 25),
P4 LANDS modulo the two §4 precision notes (pp 5/15/0/2; DS 94 + Q72 alone;
verdicts unchanged; 19-plan set adjudicated per-row). No P-miss stands: no
DPTRACE re-audit was triggered because no prediction about the mechanism
missed — the arm fires exactly on the measured hit shape and nowhere else
(Q5/nation-outer and all non-probe rels bit-identical). (Review precision:
strictly P4 missed twice — "~7823" magnitude and "no NEW shape flip" — with
a full mechanistic account for both; the re-audit substance a DPTRACE round
would supply is already covered by gate 3, where joins are bit-identical
and only Yao-input lines move.)

## 6. Ledgered follow-ups (unchanged + 1 addition)

- R62-#1 corner (restricted + parameterized same rel skips Yao entirely —
  over-count toward the tuples clamp; no live case known; any consistent gate
  movement triggers re-audit), rounding parity (10666 vs 10667),
  LowKey/HighKey/SAOPKeys probe detection.
- Unchanged queue: (a) Materialize producer, (b) Q4 (cost-model framing —
  goopg shape cheaper; NOT an estimator round), M1-display, NLI staleness
  comment, R61 #4 (CTE-body tagging), R61 #5 (executor watch).
- **NEW #6**: large-group strategy/stats framing — (i) hash costing at large
  group counts favors GroupAgg+Sort where PG keeps HashAgg (TPC-H Q3 @
  ~307k groups: rows now PG-close, strategy diverges); (ii) column-nd gaps
  cap the lift where the estimator is now faithful (DS-Q39 body 3202 vs PG
  7823). Both are downstream of correct counts — cost-model/stats scope,
  not estimator scope.

Evidence tmp-only `/tmp/pp2/r62/` (tops/values/pp, `dp-r62.txt`, patched
`sweep-r62.sh`, ds05/ incl. `sweep-20260911-123234.txt` +
`plans-20260911-123234.txt`); clone `/tmp/pp2/clone-ds05-r62` :5535;
binaries `/tmp/pp2/bin/goopg-r62` + `goopg-r62fresh` (md5-identical
`a252a3ab…`).
