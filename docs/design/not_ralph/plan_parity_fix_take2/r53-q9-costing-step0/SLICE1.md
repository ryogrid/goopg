# R53 Slice-1 — L6 hash-arm attribution (report, 2026-09-10)

*Step-0 REPORT §3 scoped this slice: name the 632364.99 rival as PG's
partition, read PG's price of that partition, decompose winner-vs-rival
into arm terms, rule on the outer-WIDTH hypothesis. Verdict: the rival IS
PG's partition `{o}|{l,n,p,ps,s}` (best orientation: probe orders);
the 2.5% margin decomposes EXACTLY into four arm terms; the width
hypothesis is CONFIRMED in its M0076-qualified form — width rides
build-side memory (spill pages ∝ column count), per-tuple CPU is
width-free by construction. No planner behaviour changed: the deliverable
is the instrument (DPPATH `outer=`/`inner=` fields, trace-only) plus these
numbers.*

## 0. Method

Instrumented worktree binary `/tmp/pp2/bin/goopg-r53slice1` (this round;
`path.go` + 6 join constructors + `pathtrace.go`, test in
`pathtrace_test.go`). Capped scratch server (`GOOPG_CG_UNIT=r53slice1`,
`scripts/goopg-test-run.sh`) on `/tmp/pp2/clone-tpch` :5534 with
`GOOPG_PGSHAPED_DP_TRACE=1`; same hand-written Q9 (`q9.sql`) via psql
EXPLAIN, twice — byte-identical plans (`PLAN-IDENTICAL`), so the trace is
stable. Evidence (tmp-only, kept): `/tmp/pp2/r53/{q9-slice1.plan,
q9-slice1.plan2,server-slice1.log,q9-pg.plan}`. Bit map (leaf DPPATH
rows): 0=part, 1=supplier, 2=lineitem, 3=partsupp, 4=orders, 5=nation.
`work_mem` = 64MB on both engines, observed via live psql `SHOW` during
the measurement window (terminal output — not captured to a file, so
cited as recorded-at-run-time; the spill geometry in §§3–4 is
independently consistent with 64MB and does not assume it).

## 1. Attribution: all 8 L6 partitions named (best orientation first)

| # | partition (best orientation: probe × build) | best total | flipped total | Δori |
|---|----------------------------------------------------------|-----------|--------------|------|
| 1 | `{p,s,l,ps,o}` × `{n}` | **616861.02** accepted | 819348.96 | +202488 |
| 2 | `{o}` × `{p,s,l,ps,n}` (PG's partition) | 632364.99 | 643216.79 | +10852 |
| 3 | `{s,n}` × `{p,l,ps,o}` | 688957.75 | 861223.95 | +172266 |
| 4 | `{ps}` × `{p,s,l,o,n}` | 900688.42 | 907719.35 | +7031 |
| 5 | `{s,ps,n}` × `{p,l,o}` | 994746.89 | 1001777.82 | +5031 |
| 6 | `{l,o}` × `{p,s,ps,n}` | 1740362.43 | 3761153.25 | +2.0M |
| 7 | `{s,l,o,n}` × `{p,ps}` | 1822324.13 | 4889466.95 | +3.1M |
| 8 | `{s,l,ps,o,n}` × `{p}` | 3757061.29 | 7361458.02 | +3.6M |

(The log's 16 kind=3 offers per run are exactly these 8 partitions ×
2 orientations. Full lines: server-slice1.log `kind=3`.)

Row 2 is the Step-0 "nearest hash rival", now NAMED: the unordered pair
`{4}|{0,1,2,3,5}` = `{orders}|{lineitem,nation,part,partsupp,supplier}`,
i.e. PG's L6 partition (§2 confirms PG joins exactly this pair at top).
Best orientation probes with orders (632364.99); flipped probes with the
5-way (643216.79). Step-0's bound is now an attribution. Margin to
winner: 632364.99 − 616861.02 = **15503.97 (2.51%)**.

## 2. PG's price of the same partition (read-only oracle :65432, trust socket)

PG Q9 top node: `Parallel Hash Join (cost=44343.58..77747.55
rows=75748 width=81)`, Hash Cond `orders.o_orderkey =
lineitem.l_orderkey` — probe orders (375000 rows, width 14), build the
`{l,n,p,ps,s}` nested loop (97740 rows, width 55, total 43121.83).
PG startup = 43121.83 + 1221.75 (= 0.0125 × 97740, the linear build-op
EXACTLY — PG charges this partition ZERO spill: 97740 × ~60B ≈ 6MB fits
64MB). PG run = 33403.97 = probe streaming 31564 + probe-ops ~1840
(linear 1695 + ~145 bucket/clamp).

Structural contrast (not absolute — rows differ: PG 75748/97740 vs goopg
303093/303093; widths differ: PG byte widths 14/55/81 vs goopg full
tuples 448/~1100/1096):

- PG: build-input 97% of startup, probe streaming 94% of run, match ~5%.
  A no-spill join.
- goopg rival: build-input 68% of startup + build-op 32%
  (~30.7% pure spill pages); probe-input 13% of run +
  spill-amortised match 87%. A spilling join.

At identical 64MB work_mem, PG's byte-width build (6MB) fits while
goopg's column-count build (~743MB ≈ 303093 × ~2453B EntryBytes) spills
11.6× over budget. The footprint model (48 B/datum,
`hashsize.EntryBytes`), not the row estimates alone, is what puts the
rival over the spill line. (Row gap is real too: 303093 vs 97740 —
sizing stays exonerated for the goopg-INTERNAL decision per Step-0 §2,
but it scales the spill cross-engine.)

## 3. Decomposition: winner vs rival into arm terms (exact)

Model (`hashJoinCost`, cost_funcs.go:630): `build = (cpuOp + cpuTuple) ×
innerRows + inner.Total` (+ `seqPageCost × innerPages` iff NBatch > 1);
`startup = outer.Startup + build`; `run = outerRun + cpuOp × outerRows +
cpuTuple × outRows` (+ bucket-walk iff stats + spill run-pages).
cpuOp=0.0025, cpuTuple=0.01, seqPage=1.0 (defaults; no SET in q9.sql).
Single equi key ⇒ residual empty ⇒ qpqual 0 on every L6 hash offer.
No ANALYZE on the bench cluster ⇒ `innerBucketSize=0` ⇒ bucket-walk
skipped. Inputs from DPPATH accepted lines + plan:

| term | winner (probe L5 612691.93/285518.00; build nation 1.25) | rival-A (probe orders 43435/0; build L5′ 200877.40) | Δ (rival − winner) |
|---|---|---|---|
| build-input total | 1.25 | 200877.40 | +200876.15 |
| build-op (linear + spill pages) | 0.31 (25 rows, 12KB — no spill) | 94546.66 (linear 3789 + spill ≈90758) | +94546.35 |
| probe-input total | 612691.93 | 43435.00 | −569256.93 |
| probe-ops (linear + spill run) | 4167.53 (linear 3789 + 379 unattributed, 0.06% — recorded, not chased) | 293505.93 (linear 6781 + spill-run ≈286725) | +289338.40 |
| **total** | **616861.02** | **632364.99** | **+15503.97 ✓** |

(The Δ column sums to 15503.97 exactly: 200876.15 + 94546.35 −
569256.93 + 289338.40.)

Reading: the rival saves 569k by probing with cheap orders instead of
the 612k L5, and pays it back three times over — 201k build-input,
95k build (of which ~91k spill pages), 289k probe-ops (of which ~287k
spill-run amortised over 1.5M probes). The spill charges (~378k
combined) ARE the margin, 24× over.

## 4. Width-hypothesis ruling: CONFIRMED as qualified (M0076 form)

Standing hypothesis (REPORT §3): "the outer-WIDTH term in the hash arm".
Ruling: width decides THROUGH build-side memory, not through per-tuple
CPU — which is width-free by construction (cpuOp/cpuTuple constants;
probe-op has no width or table-size term).

- Build-op/row: 0.312 (303093-row, ~50-col build) vs 0.077
  (1500000-row, ~11-col build) — 4.0×/row for ~4.5× columns. The linear
  CPU part is identical/row, so the gap is ALL spill pages ∝
  `EntryBytes` ∝ `ncols`. Width signal, isolated. (Review catch: the
  flipped offer's build-op is 166606.70 − 7375.70 − 43435.00 = 115796.00
  — the first draft subtracted the build input but forgot the outer's
  7375.70 startup. The report's own run-side residual already used the
  right split, so this was inconsistent, not structural.)
- Orientation flip on PG's own partition (same rows both sides — the
  clean width-placement test): +10852 for probing with the wide 5-way
  instead of narrow orders. Smaller than the winner margin: width
  placement alone does not explain 15504.
- What explains it is §3's ledger: the winner is the only contender
  whose build fits (25 rows); every orientation of every 5/6-way build
  spills at 64MB under the 48-B/datum footprint. Width × rows jointly,
  through NBatch — the M0076 parenthetical ("the width term rides
  build-side memory") verbatim.

NOT claimed: the exact per-column split of EntryBytes (ncols of the
traced paths are not in the trace; ~50/~11 inferred from page
residuals), the 379 (0.06%) winner-run remainder, PG's batch geometry
beyond its exact-linear startup. All three recorded, none load-bearing:
the margin attribution (§3) uses only traced totals plus the model's
documented linear terms.

## 5. Instrument notes (for review)

- `OuterRelids`/`InnerRelids` stamped at all six join constructors
  (`pathgen.go` hash + plain NL, `joinpathsmerge.go`,
  `joinpathsmergeouter.go` 3 sites, `joinpathsparallel.go` 2 sites,
  `joinpathsnli.go`); render appended at END of every DPPATH line
  (C-03a; `-` on non-joins). Trace-only: no planner reader, zero on
  non-joins — `TestPathTraceRendersOuterInnerPartition` pins both.
- Merge arms thread relsets through `tryMergeJoinPath` /
  `tryPartialMergeJoinPath` parameters (candidates carry no relsets)
  rather than re-deriving — same Children-order convention as hash/NL
  (Children[0]=outer/probe, Children[1]=inner/build).
- Sibling audit: writer (`pathtrace.go`) ↔ test updated together; NO
  parser change needed — `enumtrace.go` ignores DPPATH (no Malformed
  risk; verified: no new Malformed in estimateaudit suite, green).
  gofmt: repo baseline is go1.25 — two touched files carry PRE-EXISTING
  newer-gofmt drift (verified via `git show HEAD:$f | gofmt -d`);
  my hunks are gofmt-clean, no `gofmt -w` run.
- Gates: new test + full `internal/optimizer` + `testutil/estimateaudit`
  green (no `-count=1`); `scripts/tpch-spotcheck.sh` SKIPs in the worktree
  (no TPC-H data dir — exit 0 per card), substituted with the stronger
  direct check for a trace-only change: the slice-1 binary's Q9 plan is
  byte-identical to the Step-0 binary's (`q9.plan` vs `q9-slice1.plan`,
  INSTRUMENT-PLAN-IDENTICAL), so the instrument alters no plan.

## 6. Debt ledger / next

- Slice-1 deliverables COMPLETE: attribution (§1) + PG price (§2) +
  decomposition + hypothesis ruling (§§3–4).
- R53 pricing-half status: winner-vs-PG-partition FULLY decomposed. The
  engine gap behind the margin is the build-footprint model (48 B/datum
  vs MinimalTuple bytes) interacting with 64MB work_mem — a cost-model
  slice (R54 candidate), NOT join-order costing. R53's question ("what
  decides: sizing or pricing") is answered: pricing, via spill, and the
  spill is a footprint-model consequence.
- Carried (unchanged): R52 §4.2 parallel half; R51 items 2–3.
