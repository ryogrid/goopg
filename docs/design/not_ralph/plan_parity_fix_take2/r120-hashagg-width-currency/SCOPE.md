# R120 SCOPE (rev 2) — HashAggregate spill arm is priced in the wrong currency: `avgVarBytes` supplied where a TUPLE WIDTH is meant

Rev 2 remediates a BLOCK review (B1–B3 + notes 1–6, all applied; the
review's confirmations of the defect, the currency claim, the Arm-B
mapping and the scope discipline are retained).

Anchored on the R120 baseline re-taken 2026-09-14 (TODO.md): TPC-H
**6/15/0/1/0**, TPC-DS **2/69/0/25/3/0** (reproduces R94 exactly — no
drift across R96–R119). `aggregation-strategy` is the **2nd-largest
TPC-H category (10/22)** and **4th-largest on TPC-DS (69/99)**;
`sort-strategy` (9 / 76) is its documented co-traveller.

Scope discipline: this changes **no Datum constant and no Datum model**
— the R114 user-stop on the Datum-size / cost-family investigation
stays closed. It does not bundle the K65/K66 width-narrowing work (§6).

## 1. The defect (source-verified both sides)

`costAgg`'s hashed spill arm (`internal/optimizer/cost_funcs.go:482-503`,
added by R3) passes **`inAvgVarBytes`** — the variable payload ONLY —
into two places whose parameter means *the input tuple width*:

| site | goopg passes | intended quantity |
|---|---|---|
| `cost_funcs.go:483` | `hashAggEntrySize(nAggs, inAvgVarBytes)` | param is literally named `tupleWidth` (`cost_funcs.go:513`) |
| `cost_funcs.go:493` | `pages := tuples * inAvgVarBytes / blockSizeBytes` | its own comment says `relation_byte_size(input_tuples, input_width)` |
| `cost_funcs.go:482` | gate `inAvgVarBytes > 0` | a fixed-width input has a real footprint; the arm is skipped entirely |

PG 18.3 (read-only oracle) passes **one and the same `input_width`** to
both: `hash_agg_entry_size(..., input_width, ...)` (`costsize.c:2801-2802`,
param declared `:2688`) and `pages = relation_byte_size(input_tuples,
input_width) / BLCKSZ` (`:2824`). The SORTED rival uses that identical
currency: `cost_tuplesort` `input_bytes = relation_byte_size(tuples, width)`
(`:1903`), with `relation_byte_size = tuples * (MAXALIGN(width) +
MAXALIGN(SizeofHeapTupleHeader))` (`:6452-6456`);
`hash_agg_entry_size` is `nodeAgg.c:1701`.

goopg's SORTED rival pays the full Go-Datum footprint:
`groupingpaths.go:436-439` → `sortPathForBounded` → `costSortRunWithWidth`
→ `hashsize.EntryBytes(ncols, avgVarBytes)` = **`48*ncols + 24 + avgVarBytes`**
(`cost_funcs.go:322`; `internal/executor/hashsize/hashsize.go:151`,
`DatumBytes=48` `:46`, `RowSliceBytes=24` `:51`). The HASHED rival
(`groupingpaths.go:384-387` → `costAgg`) pays bare `inAvgVarBytes`.

**Same input rows, two currencies.** On a 9-column row with
`avgVarBytes ≈ 40`: sorted `48*9+24+40 = 496` B/row vs hashed `40` B/row
— a **12.4x discount to hash**, rising to *infinite* when
`avgVarBytes == 0` (fixed-width input skips the arm via `:482`).

### 1a. What this cut does and does NOT achieve (B2)

It restores **goopg-internal consistency between the two rivals** — the
invariant PG maintains. It does **not** achieve PG *alignment*: after
the cut goopg's per-group entry is still roughly **6–7x PG's** on the
same row, because `DatumBytes=48` per column dwarfs PG's real
per-column bytes. Closing that residue is the K65/K66 ncols-narrowing
family (§6), explicitly not this round.

**Consequence, stated plainly:** on WIDE inputs this cut can convert
today's under-charge into an **over-charge** relative to PG. That is a
real failure mode with a named victim (Q10, §4a), and the round's
predictions are written so that "currency fix alone overshoots and must
be paired with ncols narrowing" is a **permitted, reportable outcome**,
not a failure to be tuned away.

## 2. Why this is the live mechanism

The under-stated width does not merely shrink a charge — it **switches
the arm off**. `hashAggSetLimits` early-returns when
`inputGroups*entrySize <= hashMemLimit` (`cost_funcs.go:539`), collapsing
`nbatches` to 1 and `depth` to 0; the arm then "charges exactly nothing"
(`cost_funcs.go:~531`, the `hashAggSetLimits` doc comment — the `:479-481`
comment says "INERT below the memory threshold"). `hashAggEntrySize`'s
own doc names the failure mode: *"Getting it wrong can only UNDER-charge,
never invent a spill that PG would not see"* (`:511-512`).

**Corrected baseline (B1).** The `1x/133x` figure in `cost_funcs.go:466-467`
is a **pre-R3, SF0.5** number frozen in a comment; it is NOT this
corpus. Measured OFF census on the round's own captures:

| corpus | goopg GroupAgg | goopg HashAgg | PG GroupAgg | PG HashAgg | PG MixedAgg |
|---|---|---|---|---|---|
| TPC-DS SF0.25 | **22** | **129** | **126** | **38** | 4 |
| TPC-H | 6 | 13 | 8 | 10 | 0 |

So R3 plausibly moved GroupAgg 1→22 and the arm is **not corpus-wide
inert** — the claim "the ratio never moved" is withdrawn. What remains
is a **5.7x deficit** in GroupAggregate and a **3.4x excess** in
HashAggregate against PG on SF0.25, which is the gap this round tests.

## 3. Budget pinning (B3) — required before any arm is meaningful

Firing is decided by `inputGroups*entrySize <= hashMemLimit`, so
`work_mem` is co-dominant with the currency and MUST be pinned
identically in every arm:

- goopg `work_mem` BootVal is **512MB** (`internal/utils/misc/defaults.go:785`)
  against PG 18's 4MB — a 128x gap and a standing violation of the
  project's "GUC defaults must match PG" rule (not fixed here; recorded).
- `hash_mem_multiplier` BootVal `2.0` (`defaults.go:1342`), so
  budget = `work_mem * 2`.
- TPC-H arm: `estimate-audit` sets only `max_parallel_workers_per_gather=0`
  (`cmd/estimate-audit/main.go:358`) and inherits
  `bench/tpch/runtime_goopg/data/postgresql.conf:421 work_mem = 64MB`
  → **128MB budget**.
- TPC-DS arm: `bench/tpcds/runtime_goopg/data-sf025/postgresql.conf` has
  **no uncommented work_mem** (server default 512MB → 1GiB budget); only
  `r2-instrument/capture-tpcds.sh:9`'s session `SET work_mem='64MB'`
  brings it to 128MB. If that SET is ever dropped the arms are not
  comparable.

**Requirement:** every arm (OFF and ON, both corpora, and the spotcheck)
pins `work_mem` explicitly and identically, and the REPORT records the
per-query `groups*entry` vs budget margin for at least Q10 and the
§4a watch set.

## 4. The cut (three arms, one helper, default-off)

One helper beside the arm so the two sites cannot drift again (the
repo's recurring "sibling paths must stay in sync" class):

- **Arm A — `pages` currency (`:493`).** `tuples * hashsize.EntryBytes(inNcols, inAvgVarBytes) / blockSizeBytes`. `EntryBytes = W + 24` is the exact `relation_byte_size` per-row analogue, since goopg's `RowSliceBytes = 24` lines up with PG's `MAXALIGN(SizeofHeapTupleHeader) = 24`.
- **Arm B — `entrySize` currency (`:483`).** Pass the bare tuple width `W = 48*inNcols + inAvgVarBytes` (i.e. `EntryBytes` **minus** `RowSliceBytes`), because `hashAggEntrySize` adds its own 16-byte `sizeofMinimalTupleHeader` (`:515,:519`) exactly as PG's `hash_agg_entry_size` does (`nodeAgg.c:1706-1707`). **Review confirmed this is the PG-faithful mapping and that Arms A and B are mutually consistent under it.**
- **Arm C — the gate (`:482`).** `inAvgVarBytes > 0` → `inNcols > 0`.

All behind ONE default-off flag `GOOPG_HASHAGG_WIDTH_CURRENCY`, strict
`== "1"` (mirroring `sort_pgrelationbytes.go:9-16`), so OFF is
bit-identical to HEAD and the A/B is a flag flip on ONE binary.
Default-on is a **separate decision on this round's evidence.**

Known residue to note in the code comment (note 5): `hashAggEntrySize`
omits PG's unconditional `TupleHashEntrySize()`
(`nodeAgg.c:1726-1730`, `executor.h:165`) — pre-existing, immaterial
beside `48*ncols`, but the function claims PG fidelity.

## 4a. Named watch: Q10 (B2)

Q10 **currently MATCHes PG** with `HashAggregate rows=59038 width=684`
over a 4-way join input at `width=1330`. `ncols = 37` is not an
estimate: it is `lineitem 16 + orders 9 + customer 8 + nation 4`
(verified against the PG reference cluster's `relnatts`), and
`joinsearchlevel.go:632-636` sums both inputs' full counts, so 37 is
the number the planner actually sees.

Budget = `work_mem 64MB * hash_mem_multiplier 2` = **128 MiB =
134,217,728 B**. All bullets below derive from one stated figure,
`avgVar ≈ 334 B` (back-solved from the OFF entry; **to be replaced by
the measured value** — see the diagnostic requirement in §4b):

- OFF today: `entry = 16 + avgVar ≈ 350 B` → `59038 * 350 ≈ 20.7 MB`
  vs 128 MiB → inert.
- ON (post-Arm-B): `entry = 16 + 48*37 + avgVar = 16 + 1776 + 334 =
  2126 B` → `59038 * 2126 ≈ 125.5 MB ≈ 119.7 MiB` vs **128 MiB** —
  a margin of only **~6.5%**.
- PG's own entry for that node is `MAXALIGN(16+185) + numTrans*16`;
  Q10 has **one** aggregate (`sum`), so ≈ **224 B** → ~13 MB,
  comfortably inert. PG keeps HashAggregate.

Q10 is therefore **one avgVar wobble away from flipping and costing a
match** — the §1a overshoot made concrete. Q11 (`rows=10666`,
`width=1618`) and Q15a (`rows=10006`) stay inert at 128 MiB but would
fire at PG's own 8MB budget; both are watches, not predictions.

## 4b. Diagnostic requirement (makes §4a and P5 measurable)

Plan text carries only `rows` and `width`; `ncols`/`avgVar` are planner
internals. The flag's ON and OFF arms must therefore emit, per
aggregate node, the tuple `(query, groups, inNcols, inAvgVarBytes,
entry, groups*entry, budget, fired?)`. Every margin in §4a and the P5
selection are read from that dump, not inferred from plan-text widths.
The dump is temporary diagnostic scaffolding and is removed before the
implementation commit (R117/R118 discipline: measure, then remove).

**Budget reach check (must run before any margin is believed):** verify
`SET work_mem` actually reaches the planner —
`plannersettings.go:260` → `hashsize.HashMemLimit`. If it silently
falls back to `hashsize.DefaultMemLimitBytes` (512MB → 1 GiB) every
margin is off by 8x and the arm would read as inert for reasons
unrelated to the cut. Settle it with one `SHOW work_mem` plus one
deliberately tiny `work_mem` probe that MUST make a known grouping
fire.

## 5. Falsifiable predictions (pre-registered; any miss ⇒ STOP + re-audit)

| # | Claim | Verdict on miss |
|---|---|---|
| P0 | OFF is bit-identical to pre-round HEAD, both corpora (captures byte-identical vs the `/tmp/pp2-r120` + scratchpad MD5SUMS baseline). The OFF arm MUST be the **same binary** the baseline was taken with (`/tmp/pp2-r120/goopg-bin`) plus the flag, or P0 proves nothing about the flag | flag leaks ⇒ STOP |
| P1 | ON closes **≥20% of the OFF→PG GroupAggregate gap on TPC-DS SF0.25** — i.e. GroupAgg moves 22 → **≥43**, ceiling 126; companion floor HashAgg ≥ 38. **Companion per-query bar (the binding one): the number of TPC-DS queries whose aggregate strategy agrees with PG node-for-node must be NON-DECREASING**, reported in the same ON/OFF/PG table | per-query bar falls ⇒ STOP (the count bar alone is satisfiable by flipping the WRONG nodes — a query can stay in `aggregation-strategy` while inverting from goopg-hash/PG-group to goopg-group/PG-hash, which P2 cannot see) |
| P2 | TPC-DS `aggregation-strategy` drops by **≥5 queries** with **no NEW category** appearing on any query | new category ⇒ triage before proceeding |
| P3 | **TPC-H parity does not regress: match ≥ 6**, and none of Q1/Q6/Q10/Q11/Q14/Q15a changes aggregate strategy | any of the 6 flips ⇒ STOP; if it is Q10 crossing the §4a threshold, that is the §1a overshoot and the round reports the pairing requirement rather than tuning |
| P4 | Values unchanged ON: TPC-H digest 24/24, TPC-DS SF0.25 sweep PASS=96 MISMATCH=0 | ANY values move ⇒ STOP (costing-only cut) |
| P5 | **Negative control** (note 2): a grouping whose `groups*entry_ON` lands between **60% and 90% of the pinned 128 MiB budget**, selected from the §4b OFF diagnostic dump (NOT from plan text) and named with its computed margin in REPORT.md *before* the ON run, does not change strategy | it moves ⇒ the arm is firing below its own threshold ⇒ STOP |

**P1's ceiling is a triage trigger, not an automatic overshoot
verdict.** goopg emits 151 aggregate nodes to PG's 168 and cannot emit
`MixedAggregate` at all (0 vs PG's 4), so node-for-node convergence is
structurally unreachable; goopg could exceed 126 GroupAggs while being
*more* PG-like per query. The per-query companion bar is the sounder
criterion and governs.

Q4 is **inert by construction** (5 groups — `r120.plans.txt` `HashAggregate
(cost=491169.67..491169.72 rows=5 width=72)`), so it is recorded as a
sanity check, NOT as the negative control (the review correctly called
the original P4 tautological). Q4's own `aggregation-strategy`
divergence belongs to the width/rows family, not here: R72 STEP-0
measured goopg sorting 57066 rows x width 448 where PG sorts 13266 x 16
= 120x the byte volume, a 4.26x gap unreachable by a tie-break where
PG's own margin is 0.5%.

## 6. Sibling audit / deliberate non-goals

- The two rivals will still read ncols from **different sources** after
  the cut (note 3): hashed via `aggInputWidth(child) = len(child.Output())`
  (`groupingpaths.go:326-332`), sorted via `pathNCols(sub)` /
  `pathAvgVarBytes(sub)` (`joinpathsmerge.go:493` → `path.go:648-655`).
  Same *currency*, not guaranteed the same *number* when a path carries
  a narrowed `NCols` (`pathindexonly.go:143-144`). Recorded, not
  unified this round; the REPORT must flag any query where they differ.
- Arm C also opens the gate on the **PLAIN** arm (note 4):
  `groupingpaths.go:367-370` prices ungrouped aggregates as
  `AggStrategyHashed` with `numGroups=1`. It stays inert at 1 group —
  **pinned by an explicit test**, not left to assumption (Q6, Q14).
- `hashsize.Choose` (hash **join** geometry, `cost_funcs.go:760`,
  `hashjoin_pgtuplesizing.go:57`) untouched — aggregate arm only.
- Memoize (`joinpathsmemoize.go:133,135`) deliberately passes
  `avgVarBytes = 0` (`:90`) — untouched.
- R113's `GOOPG_PG_SORT_RELATION_BYTES_COST` stays default-off and is
  **not** combined with this flag (no cross-product arm).
- **Not this round (K65/K66):** `Path.NCols` narrows at exactly ONE
  production site (`pathindexonly.go:143-148`); all other paths fall
  back to `relNCols` = full leaf schema (`joinsearch.go:397`), and join
  rels sum both inputs' full column counts (`joinsearchlevel.go:632-636`).
  The narrowing machinery (`narrowoutput.go`: `neededKeepSet` `:789`,
  `joinKeepSet` `:217`, `buildKeepSet` `:638`; `deriveJoinKeeps` at
  `createplanroot.go:122`) is **live but entirely post-selection** — it
  mutates built nodes at `createPlan` time and no cost function calls
  it, so the planner costs a join at full concatenated width while the
  executor builds a narrowed hash table. The seam
  (`relfromjoinlist.go:699-707`; `stampNeededColsOnRels()` already lands
  `NeededCols` at `:699`, paths are costed at `:707-709`) is confirmed
  **still open at HEAD**. That is the R121 candidate, and §1a/§4a say
  why R120 may turn out to *require* it.
- `work_mem` BootVal 512MB vs PG 4MB (§3) is a real GUC-parity defect;
  recorded here, fixed in neither this round nor by tuning.

## 7. Gates (all FOREGROUND, per goal instruction)

1. `go test ./internal/optimizer/ ./internal/executor/` green; `go vet` clean.
2. Focused pins: entry-size currency, pages currency, the `inNcols>0`
   gate, the PLAIN-arm-stays-inert pin, and an OFF-is-bit-identical pin.
3. `scripts/tpch-spotcheck.sh` PASS (work_mem pinned per §3).
4. Values: TPC-H digest 24/24 + TPC-DS **SF0.25 sweep PASS=96
   MISMATCH=0** (P4). Note: TODO.md's policy line still says "SF0.5
   PASS=95"; SF0.25/96 is the protocol every round since R94 has used
   (note 6) — this round uses SF0.25/96 and flags the stale policy line.
5. Parity both corpora, OFF and ON, vs the R120 baseline (P0–P3):
   TPC-H via `estimate-audit -plan-only` (**per-connection stats — never
   the raw psql script**), TPC-DS via `capture-tpcds.sh`, work_mem
   pinned identically per §3.
6. `make plan-gate` triaged, not merely observed.
7. REPORT.md (incl. the ON/OFF/PG aggregate census table, the Q10
   margin, and the P5 control) → agent review → `commit -n` + push.

Baseline captures: `/tmp/pp2-r120/` and a checksummed copy under the
session scratchpad `r120-baseline/MD5SUMS` (guards the documented
`/tmp` peer-cleanup hazard).
