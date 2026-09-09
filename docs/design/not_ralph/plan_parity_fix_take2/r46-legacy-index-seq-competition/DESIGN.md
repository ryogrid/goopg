# R46 — Cost the legacy funnel's index-vs-seq choice (K98)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Scan-type axis. Chosen by nearest-miss ranking (R43's method): TPC-DS
Q9 differs from PG on `scan-type` ALONE — the nearest miss in either
corpus — so this round can produce the programme's first TPC-DS
MATCH.*

## 0. Baseline (HEAD `519e5d77d`, canonical data, pinned env)

- TPC-H: `match=1 shapediff=19 missingnode=2`,
  `join-order=20 join-method=11 scan-type=13 parameterisation=6
  aggregation-strategy=16 sort-strategy=10 parallelism=15
  qual-placement=4 rendering=7`.
  Captures: `/tmp/pp2/oc-tpch2-goopg.txt` vs `bench/tpch/plans-pg`.
- TPC-DS: `match=0 shapediff=70 missingnode=26 error=3`,
  `join-order=95 join-method=66 scan-type=74 parameterisation=35
  aggregation-strategy=79 sort-strategy=83 parallelism=89
  qual-placement=12 rendering=33`.
  Captures: `/tmp/pp2/oc-ds05-goopg.txt` vs `bench/tpcds/plans-pg`.
- Discrepancy recorded: R43 claims TPC-H `match=2` with Q6 matching;
  unreproduced on canonical data (Q6 = `[join-order,
  aggregation-strategy, parallelism]` here; PG fixture Q6 is fully
  serial). Probably different data or the broken capture pipeline
  (`capture-tpch.sh` references deleted `/tmp/parity-r0/queries`
  files). My figures above are authoritative for canonical data.
- Oracle note: TPC-H PG fixtures contain ZERO Gather/Parallel —
  captured serial — while TPC-DS fixtures are parallel. TPC-H
  `parallelism` (15) cannot score against these fixtures; re-capture
  with parallelism on is an owner decision, not this round.

## 1. Root cause (K98)

TPC-DS Q9's only divergence:

```
scan-type: Index Scan reason_pkey on reason vs Seq Scan on reason
```

`reason` is 1 page / 35 rows. PG seq-scans it (index descent +
random heap fetch loses to 1 sequential page); goopg index-scans
it. goopg's displayed index cost is `0.00..0.01` — not a close
call, an unpriced one.

Chain, all verified in-tree:

- One-relation statements never enter the path search with
  `GOOPG_ONEREL_SEARCH` off (default; `makeRelFromJoinlist` early
  return, C-19h; knob admits one-rel problems at
  `onerelsearch.go:45-61`), so Q9's top scan carries no stamped
  `PlanCost` and renders via `DeriveLegacyDisplayCost`, whose
  `default:` arm (`plancost.go:154-160`) prices every childless node
  at `cpuTupleCost × rows`. Display 0.01 is the symptom, not the
  choice. (With the knob on, the search's one-relation protocol
  already picks access method on cost; the funnel comparison must
  agree with it — one sentence, recorded so the two paths don't
  diverge by flag.)
- The choice is made in the legacy funnel
  `planIndexScanFromWhereShape` (`planner.go:10064`): SAOP, equality
  (`planner.go:10226` area), and range arms each return an
  `IndexScan` **unconditionally** on shape match. No cost is
  consulted. (The correlated arm is the exception: M0134-0185 gave
  it a bitmap-vs-index priced choice via `bitmapOverCorrelatedProbe`
  — the in-tree precedent this round extends.)
- The cost functions to decide it already exist and are the same
  ones the search uses: `costIndexScanCore` (`costindex.go:227`)
  and `costSeqscan` (`cost_funcs.go:192`, i.e. cost_seqscan).
  Every input is available at the funnel: relpages/reltuples from
  the catalog table, index pages/tuples/tree height from the
  catalog index (with the `estimateIndexGeometry` synthesis where
  ANALYZE never visited, per R30), selectivity from the estimator
  on the qual, numQualOps from the restriction qual.
- Declining is free: `planIndexScanFromWhere` documents that
  returning `(nil,false)` lands the caller on "the Seq Scan PG
  lands on".

PG-faithfulness: this is `add_paths_to_base_rel`'s competition
(seq path vs index paths, `add_path` picks) reproduced at the one
site the legacy planner still owns. No forced shape: the cheaper
priced candidate wins, whichever it is.

## 2. Change

Equality arm only (review 2026-09-10: range/SAOP need residual-qual
accounting and descent products with zero corpus payoff on the
EXTRA axis — named follow-ups, not this round).

**Two producers, not one** (found during implementation — the
review missed the second too). `planIndexScanFromWhereShape` builds
the IndexScan, but `rewriteScanInputsWithSingleTablePredicates`
(`scan_input_rewrite.go`, called at `planner.go:1632`) absorbs
Filter conjuncts into SeqScans and REBUILDS an IndexScan with no
cost check — undoing the funnel's decline (measured live: funnel
verdict=true yet EXPLAIN still showed Index Scan). Both sites run
the same competition now:

- Funnel equality-arm exit: decline → `(nil,false)` → caller's Seq
  Scan fallback.
- Absorber `eqKey` case: `continue` (leave SeqScan + conjunct).
  SAOP/range rewrites stay uncosted (today's behavior).
  The pass returns early on searched trees, so search-planned
  subtrees keep their costed choices; only legacy subtrees are
  affected. `PlannerSettings` threaded through (was
  `(n, cat)`; one production caller + one test caller updated).

Correlated arm untouched (has its own priced choice). Callers are exactly three —
`planSelect` (:1421), `planUpdate` (:12675), `planDelete` (:12859)
— and the executor's `updateViaIndex` already falls through to the
SeqScan path when the scan is absent, so declining is perf-only.
Subquery inner plans are unaffected (funnel requires
`len(ctx.bindings)==1` with no outer ref, correlated arm aside).

Per-arm `indexScanInputs` derivation (equality arm):

| field | source |
|---|---|
| `relPages` / `relTuples` | catalog table (baserel pages/tuples, pre-restriction) |
| `indexPages` / `indexTuples` / `treeHeight` | catalog index via `estimateIndexGeometry` — never the raw `pg_class` index row (`reason_pkey` reports relpages=0 there; in-server `IndexRealPages` storage hook per `catalog.go:85-94`, width-guess otherwise) |
| `selectivity` | `clauseSelectivity` on the equality qual against a synthetic `SeqScan` (in-tree precedent: `costindex.go:512`) — NEVER 1.0 (the ordering-only collapse warned at struct comment :63-67 would flip every legacy probe corpus-wide) |
| `uniqueEqualityOnAllKeys` | set when the index is UNIQUE and the qual covers every key column (`costindex.go:336-338`; `reason_pkey` qualifies) |
| `correlation` | nil-safe `indexCorrelationFor` (→ 0 for un-ANALYZEd index columns, per R30) |
| `totalTablePages` | = relPages (single-rel) |
| `loopCount` | 1 |
| `numSAScans` | 1 (equality; no SAOP product) |
| seq-side `numQualOps` | 1 (the restriction qual evaluates per tuple); index side 0 (the qual *is* the index qual) |

**Reg-identifier carve-out** (found during implementation via 3
unit failures): when the index leads a reg*-identifier ARRAY column
(`regclass[]` etc.), the gate stays off (keep index). Sequential
comparison of reg* arrays is broken in the executor, twice over:
(1) `resolveExpr`'s comparison coercion drops `IsArray`
(`strings.ToLower(lt.Name)`), so the unknown literal is cast to
SCALAR regclass and `{pg_class}` misses whole with 42P01;
(2) even explicitly-cast arrays compare by display text
(OID-format vs name-format) instead of element OIDs, and
`compareDatum` carries no catalog to canonicalise either side.
Declining there would ship ERRORs/empty answers where the index
path is values-correct. PG seq-scans these shapes, so the
carve-out is a workaround with a ticket, not the goal state —
filed as **K100** (executor: sequential reg*[] comparison), which
also owns the array-aware cast-target fix. The carve-out dies with
K100. Scalar reg* probes are unaffected (per-row resolution
works — pinned by passing tests).

Scope guard: the reverse population (goopg seq where PG index:
`customer_address_pkey` ×3, `time_dim_pkey` ×3, `item_pkey` ×2…)
is search-side (multi-rel queries) or alignment noise — a different
mechanism, explicitly out. TPC-H Q18/Q21 `idx_lineitem_orderkey`
lines are out of the funnel's reach (subquery/join contexts, not
top-level one-rel) — arm identity not asserted; the byte-guard A/B
(test 3) carries that weight.

## 3. Success tests (all must hold)

1. TPC-DS Q9 → MATCH (scan-type was its sole category; under
   current cost/parallelism normalisation — PG plans Workers 3 vs
   goopg 4, normalised away today). First TPC-DS match of the
   programme.
2. `scan-type` falls on both corpora with ZERO flips in the EXTRA
   direction (no new goopg-index-vs-PG-seq anywhere; check via the
   census script).
3. Unit tests first (before corpus A/B): hand-computed seq-wins
   (`reason`: relpages 1, reltuples 35, unique pkey) AND index-wins
   (large-table equality probe — regression guard against a
   selectivity-1.0 implementation flipping everything to seq).
   Plus rewrite-pass pins both directions, and fixture
   re-baselines: 5 IOS/executor tests pinned unconditional index
   shapes on 2-row tables PG would seq-scan (R14 precedent:
   justified re-baselines) — scaled to 2000 distinct rows +
   ANALYZE (vacuum-then-analyze order; ANALYZE in the seeding txn
   is a no-op) so the index wins honestly. TOAST compresses
   fixture-bloating pads (measured: 64KB → 1 page), so scale must
   come from row counts, not widths.
3. Byte-guard: full-corpus A/B both corpora; only intended sections
   move (expect: Q9 DS; possibly small-table one-rel queries).
4. Values: SF0.5 sweep `MISMATCH=0`-class unchanged (same rows by
   construction — scan choice never changes rows; confirm, don't
   assume). TPC-H spotcheck Q12/Q13 PASS.
5. `match` on TPC-H is NOT a criterion (Q9 is DS; H has no
   same-shape case).

## 4. Non-goals / follow-ups (named, not owned)

- K99 (fidelity, verdict-neutral): goopg's Q9 top cost `0.00..0.01`
  vs PG `511731` — InitPlan/subplan costs are not folded into the
  parent (PG adds them in createplan). N1-normalised, moves no
  verdict; needs its own round.
- **K100 (executor, NEW): sequential reg*[] comparison.** Unknown
  literals coerce to the scalar reg type (IsArray dropped in
  `resolveExpr` comparison coercion) and array-vs-array compares
  display text, not element OIDs; `compareDatum` has no catalog.
  Blocks lifting the R46 carve-out. Corpus impact zero (no reg
  columns in either corpus).
- Search-side seq-vs-index (multi-rel) and Index-OnlyScan gaps.
- TPC-H fixture re-capture with parallelism on (owner decision).
- R43's `match=2` discrepancy (recorded in §0).

## 5. Evidence archive

- `/tmp/pp2/oc-ds05-goopg.txt`, `/tmp/pp2/oc-tpch2-goopg.txt`
  (HEAD baseline captures, pinned env, canonical/private-clone data)
- `/tmp/pp2/oc-ds05-diff.txt`, `/tmp/pp2/oc-tpch-diff-v.txt`,
  `/tmp/pp2/oc-ds05-diff-v.txt` (differ outputs)
- Servers: `:5552` (oc TPC-H clone), `:5553` (oc DS05 clone);
  foreign `:5545` untouched.
