# 01 — What the M0137–M0143 era learned

*Durable findings established 2026-09-14 → 2026-09-19 by the milestone
programme. Each finding names the task(s) that established it. Supersedes
nothing — `METHODOLOGY3/01-what-we-learned.md` remains the record for the
round era; this file adds what the milestone era proved on top of it.*

---

## Part A — Summary

### Findings about goopg's planner vs PG 18.3

| # | finding | established by |
|---|---|---|
| L1 | **Statistics are now PG-faithful and this moved no plan.** `acquire_sample_rows`, the block sampler, reservoir, `pg_prng`, MCV/histogram/correlation generation were ported line-for-line; `n_distinct` sign/order agreement is near-total (TPC-H 60/61, TPC-DS 120/120 columns). Corpus plans: TPC-H byte-identical, TPC-DS verdict tuple unchanged. The statistics layer was **not** the residual blocker. | M0138-0002..0009 |
| L2 | **Q9's 344× estimate gap survived the sampler — and was mostly catalog-metadata.** The sampler port moved goopg's Q9 estimate 97→146 toward the true 175, *away* from PG's 60,125. But M0142-0003i's later FK fix moved the same estimate to ~117,313 vs PG's 75,650 on a private clone — the residual was substantially **catalog-metadata-driven** (missing canonical FKs), not estimator-code-driven. The remaining gap rides on the different join tree: goopg plans an FK-indexed NL-probe chain, PG an all-Hash chain compounding `eqjoinsel` on a correlated FK chain. | M0138-0006, M0142-0001, M0142-0003i |
| L3 | **The `add_path` adjudication port was the era's largest mover.** Restoring PG's pairwise `COSTS_EQUAL` pathkey/parallel_safe/rows/tight-fuzz ordering (replacing the M0129-S1 exact-cost tiebreak) moved TPC-DS `aggregation-strategy` 71→44, `sort-strategy` 77→69, TPC-H `agg-strategy` 8→3, and TPC-H match 7→8 — in one commit. Election-order fidelity pays more than candidate fidelity. | M0141-S2b-11→13 |
| L4 | **Cost inputs must arrive before costing, not after.** M0139's narrowing machinery is correct but structurally inert: `applyUpperNarrowing` runs at `planner.go:189`, *after* `planStmtWithSettings` decides costs at `planner.go:141`. Previewing the narrowed width at cost time (agg `InputTarget` keep-list) was the only M0139-adjacent change that ever moved the metric — TPC-H match 6→8. | M0139-S1..S3, M0141-S2a/-fix1 |
| L5 | **"Admitted but never wins" is structural, not incidental.** Every mechanism completed to PG's spec in this era won zero corpus plans: Parallel Append (all plumbing landed; 0/99 occurrences), Incremental Sort (executor + EXPLAIN + reachability; 0/99), Memoize-as-driver variants, and all three absorption cost-arms (byte-identical). The cause is upstream: the *input* candidates PG feeds these mechanisms are pricier in goopg because earlier choices (partial-path generation, Gather Merge shapes) already diverge. | M0140-0006*, M0141-S2b-7/S7, M0139-0007a/b/c |
| L6 | **PG's plan elections happen at exact or near-exact ties more often than assumed.** Q9's L6 election is a *bit-exact* tie (108806.04332442369 both candidates); TPC-DS Q45's tie-flip had a 2e-12 margin — in **both** engines. When margins are this thin, enumeration/insertion order and the tie-break rule are the decision, not the cost formula. | M0142-0003a/b, M0142-0015 |
| L7 | **Some divergences live in the scorer, not the planner.** The `parity.py` CTE-scope key bug produced phantom `UNMATCHED-IN-PG` ea-ratchet findings; fixing it dropped findings 95→70. Separately, goopg EXPLAIN stamps *total* rows on partial-path nodes where PG stamps *per-worker* rows (~3.1× scorer skew). Instrument the term, never infer from the sum — now empirically extended to the diff tooling itself. | M0142-0016c/d, M0141-S2b-14 |
| L8 | **EXPLAIN ANALYZE printed cumulative rows, not per-loop averages** — a genuine PG-compat display defect that masqueraded as estimator error (qerr 9969×→~1× on the witness). Estimate audits must divide by loops before being trusted. | M0142-0004a |
| L9 | **The candidate half of join-order is done; the costing half remains untouched at scale.** `inferTransitiveEqualities` + the semi/anti DP plumbing landed (`admitSemiAnti` live at `joinsearchseam.go`), but `join-order` sits at ~14 TPC-H (last census; it drifts ±3 across the era) / ~88 TPC-DS — the residual is pricing and candidate-completeness (partial paths), not search coverage. | M0142-0001, -0008* |
| L10 | **The corpus's own data layer is now measured as non-canonical.** goopg `:65433` `lineitem` = 6,001,255 rows; PG `:65432` = 5,998,835; canonical dbgen SF=1 = 6,001,215. **Both** clusters diverge from canonical and from each other (other tables match). `:65433` also lacks the canonical 8 FKs (now in `build_schema_goopg.sh`, awaiting owner reload). A record-level goopg golden exists at `bench/tpch/runtime_goopg/tpch-golden-20260919/`. | M0142-0003d/e/i, P0-E6, 2026-09-19 golden dump |

### Findings about the instruments

| # | finding | established by |
|---|---|---|
| L11 | **The measurement pipeline is now trustworthy end-to-end.** Captures are machine-stamped (binary path/inode/sha + planner flags + pinned GUCs + stats-epoch fingerprint); `check-stats-epoch.sh` makes epoch drift a hard failure; the K18 `$$` trap is dead at source with an idempotence test; canonical capture procedure is documented (`estimate-audit -plan-only` for TPC-H, `capture-tpcds.sh` for TPC-DS). | M0137-0001/0002/0003/0006 |
| L12 | **TPC-H `parallelism` was measured out, not closed — and parallel mode is harsher.** The first parallel-mode PG reference (M0137-0017) read **2/22**; P0-E7's re-measure reads **3/22**, with `parallelism` divergent on 16/22. The serial protocol systematically understates the gap. | M0137-0017, P0-E7 |
| L13 | **Reservoir-seed variance alone flips categories.** Unpinned `GOOPG_ANALYZE_SEED` moved ±1–2 per-category verdicts on ≥5 queries at a *fixed* commit. The seed pin is now in `env_tpcds.sh`, but shared clusters loaded before the pin still carry wall-clock-seeded stats (M0137-0021 owns the re-measure). | M0138-0008, M0137-0020 |
| L14 | **Gate stamps + staged-tree hashing work.** All values gates now write `tmp/gate-stamps/*.json` binding (tree hash, binary sha256, result); the commit-msg hook enforces `CATEGORIES-EXCL-MATCH:` or `PARITY: N/A — <reason>` on planner/executor commits. The "was it measured on the staged code" question is now mechanically answered. | M0137 harness, `.githooks/commit-msg` |
| L15 | **`plan-gate` drift is structural, not accidental.** It diffs the *live* `:65433` server against the mtime-newest `plan_snapshots/` file — so it measures live-binary-vs-baseline drift, and it re-staled to 14/22 within ~100 commits of the 9/15 re-pin. Its cadence must either be automated or the instrument reframed (see 03). | M0137-0005, M0137-0022 |

### Findings about method

| # | finding | established by |
|---|---|---|
| L16 | **Recon-first decomposition is now the dominant work shape and it works** — but inert-plumbing chains are the new pathology: the semi/anti chain took ~20 slices (b1→c22) of verified-inert work before the real unblock (an IN-unnesting `.SJInfo` producer) was identified, and is now owner-FROZEN. S5's "name the expected movement" rule + S4's lineage budget caught it, but late. | M0142-0008* chain |
| L17 | **Engine-correctness dividends kept paying.** The era fixed: CHECK constraints silently not enforced after restart; per-DB type-catalog `DefaultDBOid` hardcodes; `pg_constraint` returning 0 rows post-restart; `int4[]` heap misencoding; a WAL ring-integrity wedge under multi-backend load; the `instrumentScope` package-global data race; EXPLAIN cumulative-rows. The parity programme remains the project's best bugfinder. | M0143-0001..0008, P0-E5, M-NIGHTLY-* |
| L18 | **Floors held.** Across ~180 milestone tasks: TPC-H match never dropped below 8 after it was reached; TPC-DS never below 2 on SF0.25. The non-regression floor (M4's one surviving match clause) worked as designed. | P0-E7 bulk re-measure |

---

## Part B — Detail

### B1. Statistics faithfulness (M0138) — the full result

The milestone ported PG's `acquire_sample_rows` machinery line-for-line into
`internal/executor/analyze_block_sampler.go`: `pgPRNGState` (xoroshiro128**),
block sampler (Algorithm S), reservoir (Algorithm Z), PG's `RowCount`
extrapolation `floor(liverows/blocks_sampled × totalblocks + 0.5)`, the
sampled-live-row `AvgWidth` denominator, MCV/histogram/correlation from the
shared sample, `sort.SliceStable` correlation tie-break, MCV bucket ordering,
the `nmultiple` candidate cap, and real `VARSIZE_ANY`-equivalent numeric width.

Measured outcomes (M0138-0005/0006):

- `n_distinct` sign agreement near-total; `l_orderkey` ndistinct moved from
  ~1.17M (3.4× off) to 327,804 — just below PG's own unpinned noise band
  (336,410–366,886). The gap had been the **sampler**, not the convention.
- **Zero TPC-H plan movement**; TPC-DS verdict tuple unchanged; ±1 sideways
  category moves on Q21/Q48 only.
- Q9's estimate moved 97→146 — toward the true 175, away from PG's 60,125.
  The pre-registered prediction failed and was honestly scored.
- `GOOPG_ANALYZE_SEED` verdict: keep — PG's `pg_global_prng_state` has no
  reproducibility knob; the pin is harness-only.
- New sub-findings filed: fast-path `numeric` avg_width needed a real ruler
  (fixed, 0007); correlation-banding "divergence" resolved **NOT A DEFECT**
  (0009 — ordinary reservoir variance equally present in PG's sampler).

**Consequence:** the "same statistics" clause of the goal is now substantially
satisfied. Everything still diverging is in candidate generation, costing, or
election — the statistics hypothesis is exhausted.

### B2. Where the match count actually moved

Only three changes in the era moved a match count, and their shapes are
instructive:

| change | effect | mechanism class |
|---|---|---|
| M0141-S2a-fix1 (agg `InputTarget` width preview at cost time) | TPC-H match **6→8** (Q3, Q13) | cost input arriving *before* election |
| M0141-S2b-13 (`add_path` `COSTS_EQUAL` port) | TPC-H 7→8 held; TPC-DS agg 71→44, sort 77→69 | election-order fidelity |
| M0142-0005a (Memoize-wrapped index probe as Gather driver) | Q34/Q73 flipped to **exact PG reference shapes** on SF0.25 | candidate-admission |

The pattern: **movement came from fixing the decision procedure (election,
pricing inputs, admission) — never from building a new operator.** Every
operator-level build (Parallel Append, Incremental Sort, absorption arms)
measured zero.

### B3. The "admitted but never wins" mechanism

M0140's Parallel Append is the cleanest case study. All of this landed and is
verified correct:

- `RelOptInfo.LeftBranchRel`/`RightBranchRel` threading (0006a)
- `addPartialSetOpPath` — PG `cost_append` partial-path arithmetic (0006b)
- `generateUpperRelGatherPaths` wired into `createSetOpPaths` (0006b-2)
- Executor claim-sets for `setOp` under Gather — mutation-verified (0006c)
- All four branch-driving kinds admitted (hash, NL, merge, bitmap) (0006c-2)
- Mixed partial/non-partial arm with `claimedWhole` CAS claim (0006c-3)

And every slice measured `PLAN-SHAPE: queries=99 same=99`. `Parallel Append`
appears **0 times** in goopg's 99 plans. The reasons are named and recorded:
the SetOp branch inputs are never costed partial paths the producer can win
with (Q76 additionally needs M0142-0005a's Memoize admission — which landed
and *still* didn't flip it); PG's six Parallel-Append queries (Q2/Q5/Q14/
Q71/Q75/Q76, reference-dependent) all diverge further upstream.

Incremental Sort is the same shape one milestone over: executor, EXPLAIN
`Presorted Key:` rendering, `createIncrementalSortPlan`, `buildNode`, all four
tree-walkers — landed and reachable (S2b-7 proved a real candidate now
*loses on cost*: Q3 3733.01 vs 3730.89). The corpus offers it 0/99 wins
because the *seed* candidates it would extend already differ from PG's
(parallel/`Gather Merge` shapes goopg doesn't reach).

**Lesson:** for cost-elected features, mechanism completeness is necessary
but the binding constraint is upstream input-path parity. See 03 §3.

### B4. Election-level findings

- **Exact ties are real and decide plans.** Q9's L6 near-tie measured
  bit-exact identical costs (M0142-0003b); Q45's tie-flip margin was 2e-12
  (M0142-0015). At this margin the tie-break rule *is* the plan.
- **Insertion order mattered and is now PG-faithful.** `addGroupingPaths`
  inserted HASHED before SORTED — opposite of PG's can_sort-before-can_hash
  (S2b-10); reordering alone measured zero because the M0129-S1 exact-cost
  tiebreak evicted first-inserted anyway (S2b-11); only the full
  `COSTS_EQUAL` port (S2b-13) moved categories. Lesson: layered tie-break
  deviations must be removed *together*, not one at a time.
- **`STD_FUZZ_FACTOR 1.01` genuinely decides plans** (Q4: inside-fuzz →
  hashed, outside → sorted) — confirmed still operative.
- **Partial-path `rows=` display skew**: goopg stamps total rows where PG
  stamps per-worker (~3.1× scorer artefact) — a display-convention fix filed
  as M0141-S2b-16.

### B5. The semi/anti DP-search programme (M0142-0008 family)

The largest single sub-programme of the era: ~30 slices from `0008a` through
`-c22`. What it established:

- `admitSemiAnti` is live (`joinsearchseam.go`); the seam's legality gates,
  `leafSpan` coordinate space, synthetic-leaf provenance, `SJInfo` threading
  through `Path` and `joinInfoList`, and the `chainCarriesLateral` fix are all
  landed and corpus-safe (99/99 same throughout).
- **Reachability remains ~zero**: `jointypeForDirection`'s SEMI/ANTI arm is
  entered 0 times corpus-wide; Q78 now correctly declines at the deliberate
  C-04a `outer-over-derived` firewall; the EXISTS/IN family (Q10/Q16/Q35/
  Q69/Q94) never emits a semiAnti trace — suspected `whereEligibleForPreDPUnnest`
  statement-level decline.
- The recorded real unblock is an **unfiled IN-unnesting `.SJInfo` producer**;
  the owner froze the whole chain (Q2 decision, 2026-09-17) pending the
  P0-E7 evidence — which is now in (`admitSemiAnti` A/B: 23/24 digest lines
  identical, sole divergence = Q9's 600s timeout in both arms).
- Delivered-but-unreachable work: the unique-ify builders (-3b/-3c) and the
  HASH-method unique path (-1a) are landed, unit-tested, provably unreachable.

### B6. Engine correctness dividends (M0143 + P0 + nightly)

Real defects the parity work surfaced and fixed (each with a fails-first test):

- **CHECK constraints silently stopped enforcing after any restart** — never
  written/reloaded (0003b). Plus PK `IsConstraint` reload, UNIQUE `conindid`
  persistence, named NOT NULL metadata, EXCLUDE `indisexclusion` durability.
- **Per-DB type-catalog `DefaultDBOid` hardcodes** — cross-database
  `pg_attribute` UNION corruption confirmed live; read+write sides routed
  per-DB (0002c–h).
- **`pg_constraint` returned 0 rows post-restart** — four independent
  in-memory sources decomposed and fixed.
- **`PhysicalTypeIsVarlena` missing `IsArray` arm** — `int4[]` rows were
  heap-misencoded (raw-page-verified).
- **WAL ring-integrity wedge** — reservation under-budgeting + `curr ≤ tail +
  reserved` non-invariant under multi-backend load; fixed with
  `walBufferReservationClaim` and in-`posMu` window checks.
- **`instrumentScope` package-global race** — threaded as an explicit
  parameter (was race-gate red for many loops).
- **EXPLAIN ANALYZE cumulative-rows** display defect (L8).
- **FK validation scan** — O(child×parent) unindexed, uninterruptible; now
  index-accelerated with cancellation (6.7s on 800k×200k).
- **ALTER non-CREATE DDL had no rollback-undo** — `catalogRowLive` CLOG
  consult + undo entries (P0-E5/M0143-0008).
- **COPY TO / ANALYZE named-DB scoping gap** — `COPY … TO STDOUT` inside a
  named database fails `relation "x" does not exist` (same class as the known
  ANALYZE per-DB scoping defect); found 2026-09-19 during the golden dump —
  **unfiled as of this writing** (see 02 §C1).

### B7. Cluster/data-layer findings that now constrain measurement

- `:65433` suffered two loss incidents (9/17 catalog-loss via uncommitted
  ALTER + `-mode immediate` stop; 9/18 host-global OOM); restored 9/19 from
  `preloss-clone-20260915` (itself under permanent HOLD). Recovery tooling:
  `scripts/tpch-ref-recover.sh` (owner-only), documented in
  `maintenance_prompts/cluster-ops-runbook.md`.
- The cluster's `tpch` DB **lacks the canonical 8 FKs**; the build script now
  emits them (M0142-0003i, `e919b58e2`) but a reload is owner-gated. FK
  evidence measurably changes Q9's join shape (estimate collapse resolved on
  a clone: 117,313 vs PG's 75,650).
- **Record-level divergence between the two clusters** (L10): `lineitem`
  counts differ by ~2,400 rows between goopg and PG, and *both* differ from
  canonical dbgen — so PG-side dumps cannot substitute as a goopg golden, and
  "same data" is now a measured assumption, not an axiom.
- K41's `relpages` divergence (TPC-DS `customer`/`item` dimension tables) is
  root-caused: goopg's trimmed `bpchar` storage packs tighter per page — a
  deliberate design convention whose reversal is owner-decision-gated
  (M0143-0007b).
