# 06 — goopg statistics infrastructure and selectivity estimation (current state, HEAD Sep 2026)

Counterpart of [03 — PostgreSQL statistics infrastructure](03-pg-statistics-infrastructure.md).
Sections 1–9 mirror doc 03 §§1–9; doc 03 §10 (FK/join sizes/invalidation)
is split across §§10–11 here; §§0, 12, 13 are goopg-only additions with no
03 counterpart (§12 says so in its header; `#` in §13 cites take3 03 §11).
Base:
`docs/design/not_ralph/planner_refactor_take2/06-goopg-statistics-infrastructure.md`
(2026-09-02). Re-verified at HEAD `d5f8a6ff9` with Serena symbol tools plus
spot-reads; `path:line` citations re-pinned at HEAD `b4e68c574` by file
search + targeted reads; every commit hash below was resolved with `git show`. Claims
carried forward without re-verification are marked **[carried]**; timings and
sweep figures are as measured, not re-verified.

## Landed take2 work this document absorbs

`f07c20b1f` (pg_statistic decode — three silent bugs, TPC-H −10.5%) ·
`287232e17` P1-01 (real index relpages) · `3bcac056c` P1-03 (VACUUM persist) ·
`d3e12b3b4`+`ada899c38` P1-03b (TRUNCATE reset + ANALYZE/VACUUM invalidation) ·
`85bdad317` P1-05 (relsize retirement) · `febe89168` P1-07 (ndistinct
override) · `bf2c29d95` P1-08 (hypergeometric MCV) · `36c78e28c` P1-11b (date
scalar) · `71653da23` P1-12/13 (conjunction + RangeQuery pairing) +
`13430fc3a` P1-14 (nulltestsel) · `b0097a2af` P1-15 (eqjoin MCV) ·
`ca9328ed0` P1-19 (isunique) · `7ef387324` P1-20 (equiv constants) · P1-25
(DISTINCT sizing) · `4c8ea479f` P1-26 (resolver collapse) · `86b3b96a2` P1-28
(correlation view) · `dd22e656c` (ndistinct two-form fix, −8.1%) · P2-03/P2-04
(cache-relevant sizing/guard) · P1-17 verified-satisfied · P1-09
verified-satisfied · P1-06 declined (exact reltuples kept) · P1-18 blocked on
P3-04 · P1-21 precondition restated (cap stays).

Phase-1 measurement guidance (three A/Bs, recorded in TODO): restoring
*absent* statistics moved TPC-H −10.5%; refining *inaccurate* ones did not
move it (P1-13 +0.45%, P1-14/P1-25 +0.88%, noise). Remaining Phase-1 items
are judged by the estimate ratchet, not per-item timing.

---

## 0. Prior-record corrections — all folded in, plus two new fixes

### 0.1 Haas–Stokes: ledger row 777 stale (unchanged since take2)

`ndistinctEstimate` (`operators_analyze.go`) is still the Duj1/Haas–Stokes
transcription with `toowide_cnt = 0`; `NDistinct` (absolute) +
`NDistinctFrac` (fraction) reduced on read by `StaDistinct()` with the 10%
rule **[carried from take2 §0.1]**.

### 0.2 "Stats are per-connection": false (unchanged since take2)

Process-wide, per-database, ANALYZE stats restart-durable via the
`pg_statistic` heap + `goopg_relstats` sidecar **[carried from take2 §0.2]**,
with the table amended:

| fact | status now |
|---|---|
| visible same-DB other sessions | yes, immediately (unchanged) |
| survive restart (ANALYZE) | **yes — now including histograms/MCVs** (`f07c20b1f`, §0.3) |
| …except >1-page `pg_statistic` rows | still lost (no TOAST, P1-11 open) |
| VACUUM reltuples/relpages survive restart | **yes (new, P1-03)** — was NO |
| autoanalyze stats survive restart | still NO (direct field write, no persist) |
| cached plan re-planned after ANALYZE/VACUUM | **yes (new, P1-03b)** — was NO |
| cached plan re-planned after cost-GUC SET | bypassed, not re-planned (P2-04) |
| `Correlation` / per-column `AvgWidth` survive | yes (unchanged) |

### 0.3 NEW: the statistics were on disk all along (`f07c20b1f`)

Three silent decoder bugs, each masking the next: `decodeTextArray`
advanced by unpadded element length (PG aligns to typalign);
`readVarlena` assumed the 4-byte header while the writer emits PG's 1-byte
short header under 128 bytes; `readVarlena` aligned every slot to 4 while
`stavalues*` is `anyarray` typalign `d` (8). Stats were persisted and read
back empty. `l_shipdate` now restores mcv=100 hist=101 with no new ANALYZE;
a date-range estimate moved 2000418 (= rows/3, default) → 2567922 vs PG's
~2.58M. Gate: `TestPGStatisticRoundTripPreservesHistogram`. As measured:
**TPC-H 288.10s → 257.75s (−10.5%)**, Q5 −32.2%, Q7 −17.2%, row counts
identical. Almost certainly every recorded benchmark figure (including the
9.9× headline) had been measured blind on restarted servers. P1-11 (TOAST)
remains open but must be re-measured — the wide-text case may behave
differently now.

### 0.4 NEW: ndistinct was read in one of its two forms (`dd22e656c`)

`ColumnStats` stores upstream's one signed `stadistinct` as two fields, and
both `eqSelectivityForColumn` and `resolveBaseColumn` read the absolute
field alone. Every column whose distinct count scales with its relation —
most keys — read as ndistinct **zero** and fell to `DEFAULT_EQ_SEL`:
`p_partkey IN (1..5)` estimated 5000 rows vs PG's 5. `ResolvedNDistinct(tuples)`
(`catalog.go:1913`) now applies `get_variable_numdistinct`'s convention
(`ndistinct = −stadistinct × ntuples`); both call sites use it (plus
`columnRawRowsForChild`, `selectivity.go:443`, supplying the RAW divisor —
0 means "absolute form only"). As measured: IN-list 5000 → **5** (exact);
`l_orderkey IN (1,2,3)` 90018 → **14** vs PG's 51 (1765× → <4×, safe side).
TPC-H **−8.1%**, TPC-DS aggregate −3.6%, 79 shapes moved, both runtime moves
faster, no regressions.

---

## 1. Relation-level statistics (goopg's `pg_class` analogue)

### 1.1 The structs and their writers

`TableStats` / `ColumnStats` / `MCVEntry` shapes unchanged **[carried]**;
`StaDistinct()` reduction unchanged; `ResolvedNDistinct` added (§0.4).
`isunique`: `joinVarStats` still has no `isunique` field in the selectivity
path, but the *ndistinct* path now honours uniqueness — P1-19
(`ca9328ed0`): single-column unique keys override the sampled count
(`get_variable_numdistinct`'s "assume unique" branch, selfuncs.c:6332),
reading the scan's stamped `UniqueKeys` (`SeqScan`/`IndexScan`,
`uniqueKeyColumnSets`), with `nkeycolumns == 1` exactly as
`has_unique_index` requires (Q9's two-column `partsupp` PK correctly says
nothing about its members). Safety net, not a measured win (TPC-H PKs
already reach −1 on both engines). PG's nullfrac derating in the FK formula:
still open (second half of the item).

Writers of `Table.Stats` — VACUUM row now durable, TRUNCATE row new:

| writer | durable? (delta vs take2) |
|---|---|
| `ANALYZE` (SQL) | yes (unchanged) |
| partitioned-parent roll-up | no (unchanged) |
| `VACUUM` → `UpdateRelStats` + **`persistRelSize`** (`operators_vacuum.go:268-279`, P1-03) | **yes (new)** — non-fatal on write failure; same table both arms: `222/0`+lost → `222/50000`+kept |
| `TRUNCATE` → `UpdateRelStats(tbl, 0, 0)` + `Stats = nil` + persisted zero row (`operators_ddl.go:16258-16270`, P1-03b) | **yes (new)** — was 50000-row estimates on an empty table; now the never-analyzed 10-page-floor answer (2550, exactly PG's) |
| autovacuum / autoanalyze | no (unchanged; direct field write, no lock) |
| startup reload | is the reload (unchanged) |

### 1.2 `UpdateRelStats` — now with a durable half

Merge-not-replace semantics unchanged **[carried]**; `persistRelSize`
(extracted from `persistStatsToPGStatistic`) is the VACUUM/TRUNCATE writer.
`vac_estimate_reltuples` still absent — VACUUM recounts whole relations
(P1-06 declined: exact `reltuples` kept; sampling would buy ~5.7× ANALYZE
speed for planner-input accuracy, and ANALYZE is not in the query path).

### 1.3 How the planner reads them

`estimateRelSize` transcription, `estimateTableRowsFallback` gating (now
unconditional per P1-05 — the `stage` parameter removed, not ignored),
`baseRelPages` stored-preference divergence, `relAllVisibleFraction` live-VM
numerator — all unchanged **[carried]**. `hasSubclass` still
partition-key-only (ledgered) **[carried]**.

### 1.4 Index information

P1-01 landed: `pg_class` index rows report real `relpages`/`reltuples`
(`287232e17`); the planner already consumed real pages via
`IndexRealPages`, so the synthesis is superseded for `relpages` whenever
storage answers. Remaining synthesis (P1-02, rescoped): tree height from
log-fanout, `indexTuples = relTuples` (wrong for partial indexes).
Metapage discount, partial-index tuples, `predOK` prover: still absent
**[carried]**.

### 1.5 Column-statistics access path — ONE arm list (P1-26, `4c8ea479f`)

Take2's four-resolver hazard table collapses: `columnStatsForChild` was a
second full walker and had already drifted (no `*IndexScan` arm — an
index-probed leaf resolved to *no statistics*, every clause over it a
default, while the ndistinct twin resolved fine). It is now a thin wrapper
(`selectivity.go:451`) delegating to `columnStatsForChildBase`
(`cardinality.go:899`), so the index-probed leaf gains MCV/histogram access
and future drift is impossible rather than discouraged. (The comment at
`joinkeyproof.go:120-125` still says `columnStatsForChild` has its own copy
missing the `*IndexScan` arm — stale since P1-26.) `columnStatsByName` (search coordinate space) and
`resolveBaseColumn` (legacy coordinate space) remain distinct by necessity
— different coordinate spaces, not duplicated logic. `groupUniqueNDistinct`
still the only uniqueness-to-distinct bridge besides P1-19 **[carried]**.

---

## 2. ANALYZE (`internal/executor/operators_analyze.go`)

### 2.1 `analyzeRelationWith` flow

Unchanged: every block, exact `RowCount`, Algorithm-R reservoir, decode-only-
kept (EO1-4) **[carried]**. P1-06 declined as written (exact `reltuples`
stays; revisit only if autoanalyze overhead is measured at larger SFs, and
then sampling + `vac_estimate_reltuples` scaling must land together).

### 2.2 Per-column target and type dispatch

Unchanged (no 100-floor, no per-column `targrows` raise, no `WIDTH_THRESHOLD`
— the proximate cause of the §2.11 page overflow) **[carried]**.

### 2.3–2.4 `computeColumnStats`

Unchanged except MCV (§2.5) and the override (§2.9): null-inclusive
denominators, variable-payload-only `AvgWidth`, Pearson correlation for
orderable kinds **[carried]**.

### 2.5 Which MCVs are kept — hypergeometric (P1-08, `bf2c29d95`)

Take2's largest single ANALYZE divergence is fixed: the `mcvFreqMargin =
1.25` greedy admit (which PG 18.3 does not contain — no `1.25` in
`analyze.c`) is replaced by `analyzeMCVList`
(`operators_analyze.go:483`) — the hypergeometric significance test
(`analyze.c:2980`) with upstream's complete-list-fits short-circuit
(`:2676`). Walk direction load-bearing (remove-from-full, never add-to-empty).
As measured, MCV counts: `l_orderkey` **100 → 0** (PG 0 — a ~1.5M-distinct key
had 100 bogus MCVs answering every lookup); `l_shipdate` 100 → 23 (PG 21);
`l_returnflag` 1 → **3** (PG 3 — the Q1 group column); `p_type` 0 → 4 (PG 1).

### 2.6–2.8 Distinct stats / width / index stats

Unchanged **[carried]**: `isOrderableKind` degradation; `AvgWidth` = 0 for
fixed-width types (reaches `costMemoizeRescan` and `pg_stats.avg_width`);
`compute_index_stats` absent (ANALYZE never opens an index).

### 2.9 Inheritance, stat targets, `n_distinct` override (P1-07, `febe89168`)

The §0.1 defect fixed: the override wrote only `NDistinct` while
`StaDistinct()` consults `NDistinctFrac` first above 0.1, so on most keys it
landed nowhere-read. `NDistinctFrac` now cleared alongside (upstream applies
the override to `stadistinct` itself — one field, no second to disagree).
Gate: `TestNDistinctOverrideBeatsTheSampledFraction`. Inheritance stats
still absent; `SET STATISTICS 0` still suppresses the row; `n_distinct_inherited`
deliberately unhonoured **[carried]**.

### 2.10 Autovacuum / autoanalyze triggering

Unchanged **[carried]** (`needsAnalyze` incl. `MinAnalyzeAge = 60s`
goopg-only gate; `runAnalyze` direct-write limits).

### 2.11 `persistStatsToPGStatistic` and the page-overflow loss (P1-11, open)

Unchanged mechanism (per-column best-effort, size row via `goopg_relstats`,
append-only heap) **[carried]** — but the finding must be re-measured after
`f07c20b1f`: the wide-text case "may now behave differently" (TODO P1-11c).
TOAST support still the open item; bounded-width interim needs a ledger row.

### 2.12 `AnalyzeRelationSampled`

Unchanged (`mxs = nil`, `dsCtx = nil` caveats) **[carried]**.

---

## 3. `pg_statistic` layout and friends

### 3.1 Writer and reader

Writer slots (`buildUserPGStatisticRow`) unchanged: MCV `staop1 = 98`
hardcoded, hist/corr `staop = 0`, `stacoll* = 0`, `text[]` stavalues,
`stainherit = false` **[carried]** — still structurally PG-shaped but not
type-faithful (standby consumption UNVERIFIED, as take2). Reader: decode
fixed by `f07c20b1f` (§0.3); only slots 1–3 decoded **[carried otherwise]**.

### 3.2 `pg_stats` view — correlation rendered (P1-28, `86b3b96a2`)

Take2's stale-comment bug fixed: the view rendered hard-coded NULL behind a
header claiming ANALYZE collects no correlation, while ANALYZE computed it,
the writer persisted it (stakind3), and `cost_index` consumed it (a zero
prices every index scan at `max_IO_cost`). Now renders the value, NULL when
zero (mirroring the writer's omit-zero). Gate:
`TestPgStatsRendersCorrelation`. Rest of the 17-column mapping unchanged
**[carried]**.

### 3.3 Extended-statistics catalogs

Unchanged: `pg_statistic_ext` real rows + registry; `pg_statistic_ext_data`
declared, permanently empty; ANALYZE never builds, planner never consumes
(`grep StatisticsObject internal/optimizer/` = 0) **[carried]**. P1-22/23/24
open.

---

## 4. Variable resolution — goopg's `examine_variable`

### 4.1 `getVariableNumDistinct` + isunique (P1-19)

Branch order as take2 **[carried]**, plus the isunique arm: single-column
unique keys override sampled ndistinct (§1.1). Missing arms unchanged
(VALUES/ctid/tableoid) **[carried]**; negative `stadistinct` still scales by
raw `baseRows` **[carried]**.

### 4.2 `examineJoinVar` / `resolveJoinVarColumn`

Unchanged (by-name resolution for pre-search coordinates; single-rel
power-of-two requirement) **[carried]**, plus: `joinVarStats` now carries
`typeName` (P2-12 — histogram bounds are stored strings; typeless compare
orders "10" before "9"; a missing type refuses the estimate rather than
guessing). Unmodelled list (expression stats, subquery descent, security
gate, `DISTINCT`/`GROUP BY` inference except `groupUniqueNDistinct`)
unchanged **[carried]**.

### 4.3 `get_variable_range` — still absent

No index probe to refresh endpoints; no MCV widening **[carried]**.

---

## 5. Restriction selectivity (`selectivity.go`, `cardinality.go`)

### 5.1 Constants

| PG | goopg | status now |
|---|---|---|
| `DEFAULT_EQ_SEL` 0.005 | `defaultEqSelectivity` | ✔ unchanged |
| `DEFAULT_INEQ_SEL` 1/3 | `defaultIneqSelectivity` | ✔ unchanged |
| unhandled-clause split (restriction 1/3 vs join 0.5) | same two constants | unchanged (deliberate, mirrors upstream's two-constant shape) |
| `DEFAULT_NUM_DISTINCT` 200 | `defaultNumDistinct` | ✔ unchanged |
| `DEFAULT_RANGE_INEQ_SEL` 0.005 | `defaultRangeIneqSel` (`rangequery.go`) | **landed with P1-13** — was absent |
| pattern `DEFAULT_MATCHING_SEL` | — | still absent |
| `DEFAULT_NOT_UNK_SEL` / `DEFAULT_UNK_SEL` | `defaultNotUnkSel` / `defaultUnkSel` (`selectivity.go:593-594`) | **landed with P1-14** — was absent |
| `DEFAULT_INEQ_JOIN_SEL` | `defaultIneqJoinSel` | ✔ unchanged |

### 5.2 Equality — now on resolved ndistinct

MCV-hit / MCV-miss shape as take2 **[carried]**, with the §0.4 fix:
`remainingDistinct` uses `ResolvedNDistinct(tuples)` (take2 read the raw
absolute field and saw zero on scaling keys). Still missing: unique-var
`1/tuples` arm (partially covered by P1-19 upstream of this function),
least-common-MCV cap, `var_eq_non_const` (`ParamRef` → 0.005), no-stats
`1/nd` arm **[carried]**. `OpNe = 1 − eq` (nullfrac term still dropped)
**[carried]**.

### 5.3 Range — date scalar landed, text still flat (P1-11b, `36c78e28c`)

`numericValue` now handles `date` and `timestamp[tz]` (Julian-day / epoch
scalars) alongside the numeric family; `bucketFraction` still returns 0.5
for `text`/`varchar`/`char`/`bool`/networks (pinned by test so the fallback
cannot become a silent wrong number). Measured on `l_shipdate` at three
cuts: −0.19%/−0.99%/−3.22% → −0.06%/−0.07%/−0.04% (worst case ~80×) — a
residual half-bucket removal, not the large win originally claimed (ISO
strings already sort in date order; error was bucket-bounded). No histogram
clamp, linear bin scan, `strings.Compare` fallback for text: unchanged
**[carried]**.

### 5.4 NULL and boolean tests — `nulltestsel` landed (P1-14, `13430fc3a`)

`IsNullExpr` has a real arm: `nullTestSelectivity` returns
`stanullfrac` / `1 − stanullfrac` (defaults 0.005/0.995 — the §5.1
constants). `NullFrac`, collected since forever and previously reachable
only as a subtrahend, is now read for its own predicate. Also makes
expressible the `IS NULL` term P1-13 omitted; wiring it into the pairing is
a follow-up (open). Boolean: bare const exact; bare column / `booltestsel`
still absent **[carried]**.

### 5.5 Pattern operators — still no arm

`likeprefix.go` access-path half + bucket-granular pricing of injected
ranges unchanged **[carried]**.

### 5.6 `scalararraysel` / IN-lists — same shape, fixed divisor

Literal-list disjoint sum unchanged **[carried]**; the two-form fix (§0.4)
is what moved IN-lists three orders of magnitude (the sum previously
divided by a zero-read ndistinct). OR-combine fallback, 10-element default,
`= ANY (subquery)` semi-join pricing: still absent **[carried]**.

### 5.7 `rowcomparesel` — absent **[carried]**

---

## 6. `clauselist_selectivity` — now exists (P1-12/P1-13, `71653da23`)

Take2's "the function does not exist" is refuted: `conjunctionSelectivity`
(`rangequery.go`) **is** `clauselist_selectivity` — flattened conjuncts,
per-variable range-bound pairing (`RangeQueryClause`), remainder
multiplied — replacing the inlined AND product in `clauseSelectivity`'s
`OpAnd` arm. Measured on lineitem's one-year window: 1855086 → 902018 vs
actual 910180 (2.04× over → 0.9% under); timing neutral (+0.45%, noise) —
a negative result on this corpus (driving-scan shape constrained by
Phases 3–6, not cardinality). Still open: `RestrictInfo` selectivity
*caching* (planning-speed only → Phase 6 consolidation, not a fidelity
item); `nulltestsel(IS NULL)` wiring into the pairing; extended-stats
consultation; `varRelid` scoping; pseudoconstant rule. The `reliable` flag
and its pre-filter-count gate are unchanged **[carried]** (ledger 871).

---

## 7. Join selectivity

Two estimators still run simultaneously (sibling-paths hazard)
**[carried]**.

### 7.1/7.2 `eqjoinsel` — MCV pairing landed on the inner path (P1-15, `b0097a2af`)

Every inner equi-join was priced at `1/max(nd1, nd2)` — upstream's
no-statistics fallback — even with both MCV lists present. The full
`matchprodfreq`/`unmatchfreq`/`otherfreq`/`totalsel1`/`totalsel2` formula is
ported, taking the smaller viewpoint as upstream does, indexed (not nested:
`statistics_target²` per estimate). Gates:
`TestEqjoinselInnerMCVBeatsFlatNDistinct`,
`TestEqjoinselInnerMCVDeclinesWithoutBothLists`. **Open half:** MCV
equality by rendered text, not `oprcode` (matters where text form is not
injective — float, numeric trailing zeros; semi arm shares the limit; move
together). `isdefault` carry-out for the fallback cap: unchanged
**[carried]**. Semi/anti MCV pairing (`semiPairMatchFraction`) unchanged
**[carried]**; P1-17 verified already-satisfied (MCV arm, `nd2` clamp,
`(1−nullfrac1)`, `isdefault → 0.5` all present — including the
discount-matched-MCVs-before-heuristic step that looked divergent)
**[carried]**.

### 7.3 Join-clause operator dispatch

Unchanged (`OpNe` complement, inequality 1/3, NaN→0.5 arm) **[carried]**;
SEMI/ANTI `neqjoinsel` arm still unreachable (pinned outside the search)
**[carried]**.

### 7.4 `outerJoinRowFloor`

Legacy-arm only, unchanged **[carried]**.

### 7.5–7.6 `mergejoinscansel` / `estimate_hash_bucket_stats`

Now *partially* present at the cost sites (doc 05 §5.4–5.7): merge END
selectivities, hash ndistinct bucket fraction. The estimator-side rows
(absent `mergejoinscansel` duplicates model, no bucket-skew statistic)
still read absent — the functions exist where costing consumes them.

### 7.7 Multi-pair equality pricing — ledger rows 779/781/784 stale (unchanged)

Both arms fold every pair; EC de-duplication via `oneClausePerEquivClass`
**[carried]**. P1-16 (re-diagnose Q9 with `estimate-audit`) still open.

---

## 8. Group / distinct estimation

### 8.1 `estimateNumGroups`

Transcription (per-relation product, 0.1× clamp with `relmax` floor,
Yao/Dell'Era scaling, ceil+clamp) unchanged **[carried]**; missing
(boolean ×2, volatile arm, multivariate, cross-relation collapse) unchanged
**[carried]**.

### 8.2 DISTINCT — now sized (P1-25)

Take2's "Not sized" is refuted: `estimateDistinctRows` (all output columns)
and `estimateDistinctOnRows` (ON list) go through `estimateNumGroups` —
`SELECT DISTINCT l_shipmode FROM lineitem` estimates **7**, exactly PG's
(was 6,001,255). Set-op sizing left open (`estimateSetOp` keeps its own
arm). **Latent crash exposed and fixed in the same commit:** previously
unwinnable bitmap-heap paths became winnable and segfaulted on a missing
mctx nil guard ("an unwinnable path is an untested path").

### 8.3 LIMIT / tuple fraction

`preprocessLimit` transcription unchanged **[carried]**.

### 8.4 Aggregate / HAVING

Unchanged **[carried]** (no HAVING model).

### 8.5 CTE output statistics — rescoped (P1-27)

Plain-CTE columns already resolve to base statistics (`*CTEScan` arm in
`resolveBaseColumn` — take2's "no per-column statistics" comment is stale
for that case). The real gap is **aggregated** CTE outputs (`year_total`
shape: `sum(…)` traces to no base column → 0.005/conjunct, same as PG —
plus goopg's `rows ≤ 1` body-count guard, a deviation that helps). Replacing
the guard needs genuinely derived statistics (propagation through
aggregation), a larger item; removing it without them regresses. Open.

---

## 9. Extended statistics — nothing on the planner side (unchanged)

`pg_statistic_ext` real rows, `_data` permanently empty, ANALYZE never
builds, `grep StatisticsObject internal/optimizer/` = 0 **[carried]**.
P1-22/23/24 open.

---

## 10. Foreign-key / superkey selectivity

`superkeyJoinSelectivity` (+ legacy mirror) mechanics unchanged: raw-tuple
divisor, whole-key cover, consume-once, largest-first, composite-UNIQUE
evidence, `keyImpliedRowsBound` **[carried]**. Absent list unchanged:
SEMI/ANTI variant, **`nconst_ec`** (filtered-parent FK joins
over-estimated by the filter's reciprocal), nullfrac derate **[carried]**.
P1-20's constant propagation (04 §4.3) makes the `nconst_ec` gap *reachable*
as a future concern; not needed yet (no FK `1/ref_tuples` shortcut to
double-count).

---

## 11. Plan-time invalidation, caching, cross-session and standby visibility

### 11.1 The plan cache — two take2 fixes

Key `(namespace dbOid, normalized SQL)` and single-statement eligibility
unchanged **[carried]**. New:

- **Invalidation** (`dispatch.go:3861`, `planCacheInvalidatingStmt`
  `:3875`): DDL **plus ANALYZE and VACUUM** (P1-03b) — the explicitly-
  asked-to-reconsider case. Upstream's relcache-bus route still has no
  counterpart (statement-kind trigger instead, pinned by
  `TestPlanCacheInvalidatingStmt`).
- **Cost-GUC bypass** (P2-04): `plannerSessionInputsActive`
  (`dispatch.go:2011`) = scan toggles **or** cost-GUC overrides
  (`plannerCostGUCsOverridden`, `:1979` — override-detected, not
  value-compared, so `SET x = <default>` still bypasses; pinned by test).
  All four guard sites call the one predicate. Keying (rather than
  bypassing) on the context: still open.
- Still no generic-vs-custom model, no `plan_cache_mode` **[carried]**.

### 11.2–11.3 Visibility / standby

§0.2 table (amended) governs. Heap-row WAL replay, `goopg_relstats`
goopg-private waiver, virtual `pg_class`, empty `_data` on standby, no
`pg_restore_*_stats` handlers: unchanged **[carried]**.

---

## 12. Worked examples

Hand-derived from the code above, not measured live. No doc 03 counterpart
(goopg-only section). Deltas vs take2 noted
per example.

### 12.1 `tenk1 WHERE unique1 < 1000` — unchanged

ndistinct −1, no MCVs (hypergeometric admits nothing on all-singletons —
same outcome as the old 1.25× break, cleaner reason), 101 equi-depth
bounds, `sel = 0.1001` → **1001 rows** vs PG's 1007 **[carried]**.

### 12.2 `tenk1 WHERE stringu1 = 'CRAAAA'` and `= 'xxx'` — same answers, cleaner path

ndistinct 676 both engines **[carried]**. MCV: take2 assumed a 10-entry
list "for comparability" under the 1.25× rule; under `analyzeMCVList` the
near-uniform column keeps few-or-none (as PG 18.3 does), so the miss goes
straight to `mass/remainingDistinct` — now via `ResolvedNDistinct`, which
coincides here — `sel ≈ 0.00146` → **≈15 rows**, PG ≈15. Hit still exact
at 30. AND product still 1 row. The skewed-column cap (least-common-MCV
frequency) is still missing **[carried]**.

### 12.3 `tenk1 WHERE stringu1 < 'IAAAAA'` — unchanged divergence

`bucketFraction` still flat 0.5 for text: `sel = 0.26075` → **≈2608** vs
PG's 3077 (−15%) **[carried]**; per-bucket error shrinks with target, as
take2.

### 12.4 Date window (TPC-H Q6 shape) — FIXED by pairing (P1-13)

Take2's "highest-leverage restriction-side gap" (2.1× over-estimate from
independent bounds) no longer exists: `conjunctionSelectivity` pairs the
two `l_shipdate` bounds per variable (`hi + lo − 1` shape ≈ 0.1429 ± bucket
granularity, now with date interpolation inside buckets per P1-11b).
Measured: one-year lineitem window 1855086 → **902018** vs actual 910180.
Residual: the `IS NULL` term wiring is a follow-up; text/bounded-string
windows keep the §12.3 half-bucket granularity.

### 12.5 Join `orders ⋈ lineitem` — same agreement, tighter MCV story

Haas–Stokes arithmetic unchanged **[carried]** (−0.5% unproven, exact with
PK proven, PG agrees). MCV side now cleaner: with `l_orderkey` MCVs at 0
(post-P1-08) instead of 100 (pre-P1-08), the no-MCV branch is reached
honestly rather than despite a bogus list. The pre-`30293f788` 50×
over-estimate archaeology stands **[carried]**.

---

## 13. Fidelity table

`✔` faithful · `~` simplified · `✗` absent · `!` divergent. `#` cites take3
doc 03 §11 (42 items); `—` means the PG checklist has no standalone item.
Deltas vs take2 marked **(new)**.

| # | PG mechanism | goopg symbol | verdict | what differs |
|---|---|---|---|---|
| 6 | `pg_class.relpages` | `TableStats.Pages` | `~` | stored snapshot preferred over live (unchanged) |
| 4 | `pg_class.reltuples` | `TableStats.RowCount` | `✔` | exact (P1-06 declined; unchanged) |
| 1 | `reltuples = -1` sentinel | `Analyzed bool` | `✔` | unchanged |
| 6 | `pg_class.relallvisible` | `RelAllVisibleFunc` | `✔` | unchanged |
| 2 | `pg_class.relallfrozen` | — | `✗` | unchanged |
| 1 | TRUNCATE resets counters | `operators_ddl.go:16258` | **`✔` (new)** | renders `0` where PG renders `-1` (virtual-`pg_class` sentinel); estimate matches PG exactly |
| 2 | `vac_update_relstats` | `UpdateRelStats` + `persistRelSize` | **`~`→durable (new)** | merges correctly; now persisted, non-fatal |
| 3 | `vac_estimate_reltuples` | — | `✗` | unchanged (whole-relation recount) |
| 6 | `table_block_relation_estimate_size` | `estimateRelSize` | `✔` | unchanged |
| 6 | `relhassubclass` guard | partition-key check | `~` | unchanged (ledgered) |
| 6 | scale by live `curpages` | — | `!` | unchanged (deliberate flag-honesty) |
| 6 | `allvisfrac` | `relAllVisibleFraction` | `~` | unchanged |
| 7 | `get_rel_data_width` | declared-type widths | `!` | unchanged (P4-01 open) |
| 8 | index relpages/reltuples/height | `estimateIndexGeometry` | **`~` (new)** | **real pages win (P1-01)**; height + partial-tuples synthesised |
| 8 | metapage discount / partial tuples | — | `✗` | unchanged |
| 9–10 | stat targets / targrows | `columnStatsTarget` / `sampleCap` | `~` | unchanged |
| 11 | block sampling / Vitter / extrapolation / TID sort | full-scan + reservoir + exact count | `!`/`~`/`✔`(better)/`✗` | unchanged (P1-06 declined) |
| 12 | `WIDTH_THRESHOLD` | — | `!` | unchanged (→ §2.11 overflow) |
| 13–14 | nullfrac / stawidth / Duj1 / sign rule | `NullFrac` / `AvgWidth(!)` / `ndistinctEstimate ✔` / `StaDistinct ✔` | mixed | `AvgWidth` still payload-only; **override fixed (new)** |
| 15 | complete MCV list | `analyzeMCVList` short-circuit | **`✔` (new)** | was ✗ |
| 15 | `analyze_mcv_list` prune | `analyzeMCVList` hypergeometric | **`✔` (new)** | was `!` (bogus 1.25×) |
| 16 | MCV freq / histogram bounds / correlation | all present | `✔` | bounds ±1 value; correlation `staop = 0` |
| 18 | index/inheritance stats | — | `✗` | unchanged |
| 18 | `n_distinct` override | `columnNDistinctOverride` | **`✔` (new)** | was `!` (unread field) |
| 19, 23, 40 | ext stats build / reset / autoanalyze / autovacuum | mixed | `✔`/`✗` | unchanged |
| 20–21 | slots / staop / stavalues typing | writer | `~`/`!` | unchanged (standby UNVERIFIED) |
| — | TOAST | — | `!` | unchanged (P1-11 open; re-measure post-decode-fix) |
| — (§2.1) | transactional attstats | append-only heap | `~` | unchanged |
| 22 | `pg_stats` | `pgstats.go` | **`~` (new)** | **correlation rendered (P1-28)**; was `!` |
| 23 | `pg_statistic_ext` / `_data` | heap + registry / empty | `✔`/`✗` | unchanged |
| 24 | `examine_variable` | resolvers | **`~` (new)** | **one arm list (P1-26)**; was `!` |
| 24–25 | `isunique` | `UniqueKeys` stamp + P1-19 | **`~` (new)** | ndistinct path honours single-column keys; selectivity path still no field |
| 25 | `get_variable_numdistinct` | `getVariableNumDistinct` | `✔` | + isunique; missing VALUES/ctid/tableoid |
| 24, 26 | variable range / index probe / security | — | `✗` | unchanged |
| 27–30 | EQ/INEQ/NUM_DISTINCT defaults | constants | `✔` | unchanged |
| 27 | `var_eq_const` hit / miss | `eqSelectivityForColumn` | `✔`/`~` | **resolved ndistinct (new)**; still no MCV-min cap |
| 27–28 | unique-var / non-const eq | — / 0.005 | `✗` | unchanged (P1-19 covers part of the unique-var arm upstream) |
| 27–28 | `neqsel` nullfrac term | `1 − eq` | `~` | unchanged |
| 29 | `scalarineqsel` shape / bin search | `rangeOpSelectivity` / linear | `✔`/`~` | unchanged |
| 29 | `convert_to_scalar` | `numericValue` + date/time | **`~` (new)** | **dates interpolate (P1-11b)**; text/networks still 0.5 |
| 29 | histogram clamp / ctid arithmetic | — | `✗` | unchanged |
| 30 | `nulltestsel` | `nullTestSelectivity` | **`✔` (new)** | was ✗; pairing wiring follow-up open |
| 30–31 | bool / pattern sel | — | `✗` | unchanged (access-path prefix only) |
| 31 | `make_greater_string` | `IncrementString` | `✔` | unchanged |
| 32–33 | arraysel / rowcompare | IN arm / — | `~`/`✗` | **IN fixed via resolved ndistinct (new)** |
| 34 | `clauselist_selectivity` | `conjunctionSelectivity` | **`✔` (new)** | was `!` (inlined product); no RI caching |
| 34 | RangeQuery pairing | `RangeQueryClause` | **`✔` (new)** | was ✗ (2.1× gap closed) |
| 34–35 | range-ineq default / RI cache | **default landed** / absent | `✔`/`✗` | caching → Phase 6 |
| — | estimate provenance | `reliable` gate | goopg-only | unchanged (ledger 871) |
| 36 | inner MCV arm | `eqjoinselInnerMCV` | **`✔` (new)** | was ✗; text-equality half open |
| 36 | inner no-MCV / semi MCV+heuristic / neq / ineq-join | present | `✔` | unchanged |
| 37–38 | scansel / bucket stats | partial at cost sites | `~` | doc 05 |
| 36, 41 | multi-pair / EC dedup / INNER sizing | present | `✔` | unchanged |
| 41 | outer floors / SEMI-ANTI | legacy arm | `✔`/`~` | unchanged (P1-18 blocked) |
| 24, 39 | num_groups / DISTINCT / limit / HAVING / CTE | mixed | `✔`/mixed | **DISTINCT sized (new)**; CTE-agg rescoped |
| 40 | extended statistics | — | `✗` | unchanged |
| 41 | FK/superkey / nconst_ec / nullfrac / row bound | mixed | `✔`/generalised/`✗`/`✗`/goopg-`✔` | unchanged (+ P1-20 reachability note) |
| 42 | relcache invalidation | kind-triggered invalidate | **`~` (new)** | **ANALYZE/VACUUM invalidate**; was `!` |
| 42 | generic/custom plans / restore-stats fns | — | `✗` | unchanged |
| 42 | cross-session visibility / restart survival | shared table / heap+sidecar | `✔`/`~` | **VACUUM now durable (new)**; hist/MCV restore fixed **(new)** |

---

## Appendix — take2 claims that no longer hold

1. *"Every `WHERE x IS NOT NULL` … priced at 1/3"* — `nullTestSelectivity`
   now answers from `NullFrac`; `13430fc3a`.
2. *"No selectivity arm [for LIKE]"* — still true, but the adjacent
   *"bucketFraction returns a flat 0.5 for every non-numeric type"* is now
   false for dates/timestamps; `36c78e28c`.
3. *"There is no `clauselist_selectivity`"* — `conjunctionSelectivity` is
   it; `71653da23`. Same commit refutes *"RangeQueryClause pairing
   absent"* and the §12.4 2.1× gap, and supplies `defaultRangeIneqSel`.
4. *"No MCV pairing on the inner-join path"* — `eqjoinselInnerMCV`;
   `b0097a2af`.
5. *"Not sized [DISTINCT]"* — `estimateDistinctRows`/`estimateDistinctOnRows`;
   P1-25.
6. *"`columnStatsForChild` still lacks the `*IndexScan` arm (ledger 785)"*
   — delegates to one arm list; `4c8ea479f`.
7. *"The override is silently ignored … (live sibling-path divergence)"* —
   fixed; `febe89168`.
8. *"The 1.25× rule … largest single divergence"* — replaced by the
   hypergeometric test; `bf2c29d95` (with the `l_orderkey` 100→0 and
   `l_returnflag` 1→3 measurements).
9. *"`correlation` always NULL"* — rendered; `86b3b96a2`.
10. *"ANALYZE histograms … gone after a server restart"* (FINDING) — decoder
    fixed; `f07c20b1f` (−10.5%). The FINDING's "fix before P1-11b" ordering
    was followed.
11. *"TRUNCATE does not reset `Table.Stats`" / "VACUUM … in-memory only" /
    "ANALYZE does not invalidate"* — all three refuted; `d3e12b3b4`,
    `3bcac056c`, `ada899c38`.
12. *"The per-column `n_distinct` …" (§0.1 defect)* — fixed; `febe89168`.
13. *"New defect, not previously recorded"* (autovacuum direct write, §1.1
    row 6 of take2 appendix) — still true, carried (not refuted).
14. *Worked-example inputs assuming 1.25× MCVs (§12.2) and independent date
    bounds (§12.4)* — recomputed above under the new estimators.
