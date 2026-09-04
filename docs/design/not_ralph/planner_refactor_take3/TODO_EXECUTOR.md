# TODO_EXECUTOR — executor bundle execution checklist

Execution checklist for [13-executor-target-design.md](13-executor-target-design.md),
gated by the shared measurement contract (take3
[09-verification-and-acceptance.md](09-verification-and-acceptance.md)) plus
the executor protocol (13 §2). Evidence base:
[12-executor-gap-analysis.md](12-executor-gap-analysis.md) (gaps),
[10-executor-pg-design.md](10-executor-pg-design.md) (oracle),
[11-executor-goopg-design.md](11-executor-goopg-design.md) (current state).

Take6/take7-landed work is recorded below as `[x]` with its evidence so the
starting state is explicit; every `[ ]` item is work still to do, sequenced
per 13 §8 (EX0 first, EX1 before EX3 geometry, audit before elimination,
serial before parallel).

**One checkbox ≈ one commit.** An item that would move two executor inputs
at once is split before it is started (13 §1 EX-P3).

Legend: `[ ]` not started · `[~]` in progress · `[x]` done · `[-]` dropped
(with a reason and a ledger row) · `[!]` blocked (blocker named inline).

When closing an item, rewrite its line as:

```
- [x] EX<n>-<k> <title> — <commit>; gates: <list>; artifacts: <paths>
```

Each item names its design pointer (13 §) and its gate in 2–4 lines.
Phase-closure verdict files go under
`analysis/executor-refactor/<phase>-<date>/README.md` carrying the 13 §2.3
header, the before/after pin + timing + alloc table, and an explicit
statement of anything that got worse.

---

## Ground rules (do not start an item without these)

1. **No query-specific forcing** — gates name operators, never queries; no
   benchmark-shaped fast path (13 §1 EX-P2; take3 09 §4.3 C1–C2).
2. **Timing + allocator arms together** — a CPU win that doubles allocations
   is not a win; either arm regressing fails the item (13 §1 EX-P4).
3. **Plan-shape pin** — plan-parity instrument (or goopg-vs-goopg capture)
   before/after on both suites; executor items move no plan (13 §1 EX-P5).
4. **Both suites, fresh server per arm** — TPC-H SF=1 + TPC-DS SF0.5;
   per-query attribution + TOTAL arm are complementary (take3 09 §1
   R1/R2/R6/R7). Parallel-path items add a serial control arm.
5. **Values, never counts** for projection/join-adjacent changes
   (`-digest` + `-diff`; take3 09 §1 R8 — P4-01b faster-and-wrong).
6. **Never `-count=1`** in a gate; **never `git commit --no-verify`** for
   code (take3 09 §4.3 C5–C6).
7. **Every deferral gets a `.ralph/deferral_ledger.md` row** with a
   `postgres/` citation and a resume point (13 §1 EX-P1; take3 09 §4.3 C7).
8. **One variable per commit**; cancelling pairs move in one commit
   (13 §1 EX-P3).

---

## EX0 — Instruments (first; moves no behaviour)

*Design: 13 §2. Gates: 13 §2.3 + take3 09 §3 floor.*
Exit: ledger filed, protocol committed and used once end-to-end (Q6 chain
re-baselined), worker stats surface, per-operator slices published for the
three witness shapes, batch counters report. No plan moved, no timing
claimed.

- [x] EX0-01 File the executor backlog ledger — `6ace9567e` (design) + ledger rows this commit; gates: 8 `take3-EX0-G-EXn` rows merged, `git diff --stat` docs-only; artifacts: `docs/design/executor-ex0-01-ledger/DESIGN.md`, `.ralph/deferral_ledger.md`
- [x] EX0-02 Commit the measurement protocol — `503f12cf7` (design) + conforming artifact this commit; gates: protocol doc merged, `EX0-02-q6-serial-scold` full header (serial 5.48 s, values ×9 identical, alloc 1.06 GB/Q6, perf 56.6B instr/Q6); artifacts: `docs/design/executor-ex0-02-protocol/DESIGN.md`, `analysis/executor-refactor/ex0-02-20260903/README.md`
- [x] EX0-03 Surface per-worker hash/sort counters in EXPLAIN ANALYZE — this commit; gates: 3 new tests (`TestMergeWorkerContextMaxMergesHashJoinStats`, `TestExplainAnalyzeParallelWorkersLaunchedAndHashMerge`, `TestExplainSerialPlanHasNoWorkersLaunched`), executor suite green, serial plans byte-identical, no timing claim; artifacts: `internal/executor/{parallel_worker_ctx,instrument,context,operators_gather,operators_gather_merge,operators_explain}.go`, `internal/executor/explain_parallel_workers_test.go`
- [x] EX0-03b Per-worker rows/loops/time lines — this commit; gates: fold unit test (exact carrier) + shape/presence golden (count==launched both runs, SHAPE identical, no exact rows) + empty-carrier byte-identical pin, executor suite green, `-race` clean on all worker tests; artifacts: `internal/executor/{instrument,operators_gather,operators_gather_merge,parallel_hash_build,operators_cte_dml,context,operators_explain}.go`, tests in `explain_parallel_workers_test.go`
- [x] EX0-03c Minimal `sortOp` method/space counters — this commit; gates: 7 tests (serial main line, parallel worker lines count==launched, rescan reset, failed-Open nothing, spill external-merge, worker-0 promotion, empty-carrier byte-identical), executor suite green, `-race` clean; artifacts: `internal/executor/{operators,context,parallel_worker_ctx,operators_gather,operators_gather_merge,parallel_hash_build,operators_explain}.go`, `internal/executor/explain_sort_workers_test.go`
- [x] EX0-04 Per-operator timing harness — this commit; gates: Q6 anchor re-cut ×3 (residual-ratio 11.3% mean, null control clean, values identical), slices published Q6/Q9/Q13/Q4/Q7 + G-EX6 remainder list (decode: int/float 66%, numeric 32%), plan-gate 22/22 MATCH changed=0; artifacts: `bench/tpch/profile_slices.sh`, `bench/tpch/profile_slices_classify.py`, `analysis/executor-refactor/ex0-04-20260903/README.md`
- [x] EX0-05 Batch/width counters — this commit; gates: Q9 both arms report (witness Batches: 2, widths ~700–1100, no width≈6 degenerate), tripwire fired once (lazy-map build Build-Time-only → ledgered `take3-EX0-lazy-hash-geometry`, non-witness, gate unaffected), plan-gate 22/22 MATCH; artifacts: `analysis/executor-refactor/ex0-05-20260903/README.md` (+1 ledger row)
- [x] EX0-06 Re-baseline the Q6 chain end-to-end under the protocol — this commit; gates: Q6 serial 5.48 s + parallel 2.25 s (full capture sets), TPC-H per-query tables (252 s / 284 s), TPC-DS SF0.5 both arms PASS=95 (986.2 s / 996.5 s, ms-stamped), no behaviour change (sweep ms-patch is harness-additive); artifacts: `analysis/executor-refactor/ex0-06-20260903/README.md`

Landed foundations (record, not work):

- [x] EX0-00a Numeric-decode + take5/take6/take7 methodology — alternating
      A/B with byte-identical result gate, flamegraph/perf discipline;
      evidence: `benchmark-results-take5.md`, `benchmark-results-take6.md`
      (`:16-44,68-72`), `benchmark-results-take7.md:15-60,146`,
      `perf-optimize-take6/RESULTS.md`.
- [x] EX0-00b CLOG/atomics differential tests — hint-bit follow-up green;
      evidence: `perf-optimize-take6/RESULTS.md:131-172` (11 §17 row 15).

---

## EX1 — Tuple narrowing (touch less of the row)

*Design: 13 §3. Gates: 13 §2.3 + values-diff (R8) + plan-shape pin.*
Exit: decode/detoast proportional to referenced columns on witnesses; Q9
narrowed width recorded; values clean; plans unchanged. Consumes planner
P4-01 target lists — general fix waits on P4-01 (13 §8.6).

- [x] EX1-01 Scan projection pushdown — this commit; gates: 30 unit tests + poison runs, TPC-H values 24/24 MATCH, TPC-DS PASS=95, plan-gate 22/22, Q6 3.2–4.0 s (vs 5.48 s baseline), alloc 0.97 GB (vs 1.06 GB); artifacts: `internal/executor/{scan_deform,executor,operators_storage,expr,exprnode,operators_bitmap}.go`, `internal/executor/scan_deform_bound_test.go`
- [x] EX1-02 Deform-some-attributes — this commit; gates: join/project unit tests + poison runs (incl. unrebased-semi regression), TPC-H 24/24 MATCH, TPC-DS PASS=95 (Q16 CKMISMATCH found, root-caused to index-space key split, fixed positionally, checksum-verified), plan-gate 22/22, Q6 pinned 3.1 s, Q9 14.5 s (vs 18.4 s); artifacts: `internal/executor/scan_deform.go`, tests, `docs/design/executor-ex1-02-join/DESIGN.md`
- [x] EX1-02b Index/bitmap/IOS bound threading — this commit; gates: 21 index/bitmap/NLI/IOS subtests + poison/lossy runs, TPC-H 24/24 MATCH, TPC-DS PASS=95, plan-gate 22/22, Q6 3.02 s unchanged, Q4 1.54 s, Q13 6.80 s; NLI probe-key outer hole found in review and fixed with regression test; artifacts: `internal/executor/{scan_deform,executor,operators_index,operators_bitmap,operators_indexonly}.go`, `internal/executor/index_deform_bound_test.go`
- [-] EX1-03 Lazy detoast — DROPPED per owner direction 2026-09-04 (see Dropped table + ledger `take3-EX1-03-dropped`); design `docs/design/executor-ex1-03-detoast/DESIGN.md` retained as the resume point.
- [!] EX1-04 Owned-row narrowing — BLOCKED on planner P4-01 (PathTarget/projection): review of `docs/design/executor-ex1-04-owned/DESIGN.md` proved owned-payload shortening unsafe without projection — full-row readers above retainers (`slotRow`/`Row()`/`Materialize` on join-stacked and sort-above-join shapes, null-fill width variance) break on shortened rows, and batch geometry prices schema widths. Design retained as analysis; resume when P4-01 lands. Ledger `take3-EX1-04-blocked`. (13 §8.6; EX1 exit already sequences the general fix after P4-01.)
  - 2026-09-04 unblock review: HALF-SUPERSEDED — P4-01 landed projection at the proven-safe build-side `Project` site (10→7, Batches 2→1), so the hash-build half is unblocked-but-needs-redesign (no second `[0,bound)` truncation; Cut 0 alloc arm: Q9 20.14→13.88 s, alloc 9.43→8.52 GB, values identical — `analysis/planner-refactor-take3/ex104-cut0-20260904/README.md`; Cut 1 test-only next); sort half still blocked on deferred upper-target slice (c), merged half on (a).
  - Cut 1 landed (this commit): `internal/executor/owned_build_poison_test.go` — 5 poison tests on the Project shape (narrowed-width retention, identity-decline, unknown-target fallback, corrAbove decline, prebuilt-boundary incl. witness 18→7); 9/9 with pre-existing poison tests. Cut 2 (owned-row tightening on Project-declined paths) only if a later alloc arm shows a residual.

Landed narrow fix (record, not work):

- [x] EX1-00 Copy-after-filter + 6-of-16 prefilter deform (take5) —
      11.51 → 6.63 s; `cloneRowOwned` 26.6% → 1.16% CPU; evidence:
      `benchmark-results-take5.md:20-32,276-277` (11 §17 row 3).

---

## EX2 — Clone elimination (copy only at retention boundaries)

*Design: 13 §4. Gates: 13 §2.3 + alloc arm (primary) + pin.*
Exit: alloc arm down on witnesses, timing neutral-or-better, per-seam
tripwires green, values clean, plans unchanged.

- [x] EX2-01 Retention-boundary audit — this commit; gates: audit reviewed (8-family spot-check exact; rework for truncation/memoize/gather/C6-C7 completed and verified), no code; artifacts: `analysis/planner-refactor-take3/ex201-audit-20260904/README.md` (45 sites: 18 cloneRowOwned + 14 MaterializeArena + 13 acquireRow; top EX2-02 candidates: A12/C17 virtual-row seam, C9, C10, C11; C8 scoped-caution: source aliases producer slot)
- [x] EX2-02a Ownership passing at join seams — this commit (first cut:
      C9/C10 `drainRowsCtx`/`drainRowsCtxCTID` make+copy folded into
      single `cloneRowOwned`; TID sidecar verified buffer-independent);
      gates: executor drain/join/agg + poison tests, TPC-H 24/24 MATCH,
      plan-gate 22/22, TPC-DS PASS=95 MISMATCH=0, Q15b-MAIN 25.29→20.93 s
      alloc window 12.57→11.56 GB values identical (single-sample);
      artifacts: `internal/executor/operators_join_agg.go`
- [-] EX2-02b Ownership passing at agg input — DROPPED 2026-09-04 as
      infeasible: all 12 M-sites fail sole-owner transfer on both ends
      (sources alias reused producer slot; whole-aggregation lifetimes;
      MaterializeArena already minimal, no fold/gate). Ledger
      `take3-EX2-02b-dropped` (+ WithinGroup latent-hazard follow-up).
- [x] EX2-02c Ownership passing at gather transfer (= 13 EX2-04) —
      this commit: `transferRowForQueue` (VirtualSlot fast path: fresh
      pooled buffer transfers as-is when arena-free, clone+release when
      arena-backed; other slot kinds stay byte-identical
      MaterializeForTransfer); 4 call-site swaps (G1–G4);
      gates: 4 new transfer tests + gather/parallel suite, TPC-H 24/24
      serial + 24/24 parallel (arms values-identical), plan-gate 22/22,
      TPC-DS PASS=95 MISMATCH=0;
      artifacts: `internal/executor/{parallel_runtime,operators_gather,operators_gather_merge,parallel_hash_build}.go`,
      `internal/executor/parallel_transfer_test.go`
- [x] EX2-03 Pool sizing — closed MEASURE-ONLY 2026-09-04 (this
      commit): pool-hit 1 alloc/24B/~40ns vs make 352–896B; per-row hit
      rate ≈0% by construction (retained buffers correctly never
      return); buckets 0–64 pool all widths identically so P4-01's
      narrowing only moves traffic between identical buckets; predicted
      effect on clone slice ~0. Artifact:
      `analysis/planner-refactor-take3/ex203-measure-20260904/README.md`.

Landed foundations (record, not work):

- [x] EX2-00a Clone moved after the filter (take5) — old profile
      `acquireRow` 39.2% / `MaterializeArena` 28.5% retired; evidence:
      take5 §2.1 via 11 §16 (11 §17 row 3).
- [x] EX2-00b One-copy `PageGetHeapTupleInto` + `evalExpr` boxing removal
      (take6) — alloc −95.8% (18.8M → 0.80M); 6.55 → 4.49 s, 1.46×;
      evidence: `benchmark-results-take6.md:16-44,68-72` (11 §17 row 4).
- [x] EX2-00c `VirtualSlot.Materialize()` arena routing fix — caught
      skipping the ownership boundary; evidence: `slot.go:181-188` via
      11 §3.

---

## EX3 — Spill, sort, hash batching

*Design: 13 §5. Gates: 13 §2.3 + EX0-05 batch counters + pin.*
Exit: spilling shapes I/O-dominated; batch counters match PG semantics at
both budgets; sort-compare share down; values clean; plans unchanged.
Sequenced after EX1 (13 §1 EX-P7).

- [x] EX3-01 Verify the `runtime.Stack` elimination, then price the
      remaining spill cost — this commit: elimination confirmed in-tree
      (constructors cache registry handle; zero per-row `runtime.Stack`;
      gls fast path first); reader-path cut = buffered spill reads
      symmetric with the writer (`bufio.ReaderSize` 8192; `rewind` +
      `br.Reset`; framing/codec + WaitEvent pairs untouched; only Seek
      is rewind-to-zero); reader-path audit ranked I/O > decode >
      post-decode cloneRow > WaitEvent > framing (residual spill
      single-digit %, not the 69–86% Stack era).
      Gates: spill unit + WaitEvent tests 27 PASS, TPC-H 24/24 MATCH,
      plan-gate 22/22, TPC-DS PASS=95 MISMATCH=0, spill shapes Q7
      14.97→11.48 s (−23%) Q13 6.89→4.69 s (−32%) values identical
      (single-sample); artifacts: `internal/executor/spill.go`.
      NOT gated on Q14/Q3/Q10 (STALE witnesses, 12 §13).
- [ ] EX3-02 Dense-chunk build rows — contiguous packing (`dense_alloc`
      analogue, 10 §6) replacing per-row build allocations.
      *design: 13 §5; gate: alloc arm (primary) + timing; values + pin.*
- [ ] EX3-03 Batch-file discipline per PG semantics — symmetric probe-side
      spill, batch-pair re-reads, peak near `work_mem`. Measured at both
      budgets; EX0-05 counters gate alongside time.
      *design: 13 §5; gate: counter equality + values + pin at 64 MB and
      512 MB-equivalent arms.*
- [ ] EX3-04 Sort spill runs + merge discipline — run formation on
      `flushChunk`, tape-style merge-back (logtape analogue, 10 §9).
      *design: 13 §5; gate: spilling-sort shapes; values + pin.*
- [x] EX3-05 Sort-compare fast path — this commit Cut A: `sortOp.wantCTIDs`
      gates the TID side-channel (per-row append, third perm pass,
      re-attach branch) on a consumer; markers at LockRows (unconditional)
      + project/filter/aggregate/result (CTIDExpr-gated) + slab twins;
      window/projectSet need none (eval against materialized rows);
      joins/NLI correctly stop the walk (own scan-cursor sidecars, never
      propagate sort-fed ctids); resultOp hole found in review and pinned.
      Gates: sort/lock/CTID suites incl. 3 new resultOp pin tests
      (negative-controlled), TPC-H 24/24 MATCH, plan-gate 22/22, TPC-DS
      PASS=95 MISMATCH=0, Q16 timing-neutral (0.93→0.99 sweep noise,
      0.73 steady-state — Stage 1 already took the 34%);
      artifacts: `internal/executor/{operators,operators_lockrows,operators_join_agg,executor,opnode}.go`,
      tests in `ctid_function_arg_test.go`, `operators_lockrows_test.go`,
      `sort_external_test.go`. Cut B (per-key kind specialization) queued.
- [ ] EX3-06 Skew residency + single-pass build — MCV-pinned hot keys
      (needs planner P2-11b input) + collapse two-pass/two-map build.
      Last in EX3 (13 §8.4).
      *design: 13 §5; gate: skewed-shape A/B; values + pin + alloc arm.*
- [!] EX3-07 Presorted-prefix sort input contract — BLOCKED: file only if
      planner P4-05 (Incremental Sort) activates; the dependency runs
      planner→executor here (13 §8.7). Tiebreaker: executor publishes the
      input contract first (13 §8.7). Do not absorb silently.

---

## EX4 — Expression fast path (compile every hot site)

*Design: 13 §6. Gates: 13 §2.3 + twin-parity tests + values-diff + pin.*
Exit: no interpreted hot-site slice on witnesses (serial); 1.1–1.2×-class
wins compose without alloc regressions. No query-specific forcing (EX-P2).

- [ ] EX4-01 `filterOp` compilation (serial) — stop re-interpreting the
      prefilter's predicate (`operators.go:565`; ~0.33 pp, take7 §7).
      Serial first: no planner dependency.
      *design: 13 §6; gate: double-evaluation gone from EX0-04 slice;
      twin-parity tests; values + pin.*
- [ ] EX4-02 Join residual + key compilation — `mergeResidualMatch` and
      per-`plan.Algo` key evaluators through the slab. Values-diff
      mandatory (join-adjacent, R8).
      *design: 13 §6; gate: twin-parity per arm (`join_compiled_key_test`
      pattern); values + pin.*
- [ ] EX4-03 Agg transition fast path — compile builtin-agg `transfn`
      expressions; `MIXED`/spill behaviour unchanged.
      *design: 13 §6; gate: per-row transfn slice down; values + pin.*
- [!] EX4-04 `Gather`-arm slab reachability — BLOCKED on EX5-01
      (executor-owned `Gather` arm in `buildRec`, 11 §§1, 12); the
      plan-shape pin is the planner interface, not the work item.
      EX4-01…03 proceed independently.
      *design: 13 §6; gate when unblocked: parallel filterOp compiled;
      serial arm unchanged.*

Landed foundation (record, not work):

- [x] EX4-00 Compiled scan prefilter (take7) — 4.563 → 3.792 s serial,
      1.009 → 0.838 s parallel, 1.20×; evidence:
      `benchmark-results-take7.md:15-60,146` (11 §17 row 6). Prefilter
      only; `filterOp` still interprets (11 §18 item 5).

---

## EX5 — Parallel executor (same compiled path, more workers)

*Design: 13 §7. Gates: 13 §2.3 + serial control arm + pin.*
Exit: parallel shapes on the slab path with shared builds; worker stats on
parallel EXPLAIN ANALYZE; serial arms unchanged. Planner parallelism
(P5-01…P5-08) stays in the plan bundle — pinned here, not owned.

- [ ] EX5-01 Slab parity for `Gather` — `buildRec` `Gather` arm; parallel
      queries stop falling back to legacy `Build` (11 §1). Unlocks EX4-04;
      re-proves EX1–EX4 wins on workers.
      *design: 13 §7; gate: parallel slab coverage test; serial arm
      unchanged; pin.*
- [ ] EX5-02 Shared build hardening — barrier discipline for
      build/probe phases (10 §10), cooperative-stall measurement under
      skew, worker-count scaling on Q9-class shapes.
      *design: 13 §7; gate: scaling + skew A/B; Datum-safety tests
      (`parallel_substrate_test.go:26-80` pattern); serial arm.*
- [ ] EX5-03 Gather/GatherMerge ordering + exchange — worker-sorted slices,
      leader heap merge (`SharedTuplestore` discipline, 10 §10).
      Cost-decision stays with planner P5-05 (permitted-divergence
      candidate, take3 09 §4.4 case 1).
      *design: 13 §7; gate: ordering tests; pin; serial arm.*
- [ ] EX5-04 AIO `ReadStream` decision — measurement item with two legal
      outcomes: wire with depth policy + timing/alloc gate, or
      ledger-decline (pool hints + workers suffice). Not a commitment.
      *design: 13 §7; gate: A/B or ledger row.*

Landed foundations (record, not work):

- [x] EX5-00a CLOG-horizon atomics + subxact fast path (take6) — 10.86% →
      0.54%; Q14 1.26×, Q3 1.15×, Q10 1.17× byte-identical; evidence:
      `perf-optimize-take6/RESULTS.md:17-34,63-83` (11 §17 row 5).
- [x] EX5-00b `ToLower` removal from the per-tuple path — 4.64% → absent;
      evidence: same RESULTS (11 §17 row 5).
- [x] EX5-00c Parallel hash shared build + arena transfer rules —
      `sharedHashBuild` / `parallelBuildLazyHashTable` /
      `parallel_runtime.go:31-71`; evidence: 11 §10 (mechanics landed,
      slab parity open).

## Witness shapes (referenced by gates above)

Three shapes carry most gates; EX0-04 publishes their slices, later items
diff against them. Q6-class: scan → filter → agg on `lineitem` (identical
plan both engines, decode/clone/expr leverage). Q9-class: multi-join hash
cascade over ~6M-row builds (width/cascade/shared-build leverage).
Q4/Q7/Q13-class: spilling hash/sort past `work_mem` (spill/batch leverage).
TOAST-heavy TPC-DS shapes witness EX1-03 only.

---

## Acceptance bars (executor bundle)

Measured by EX0 instruments on both suites, S-cold and WARM (take3 09 §6
regimes). Baselines: 12 §1 (Q6 serial 3.792 s / parallel 0.838 s post-take7;
suite totals per 07 §2.1 with the honest-ratio caveat).

| bar | metric | target |
|---|---|---|
| E1 | Per-query timing ceilings | **no query slower than 1.2× its EX0-06 per-query baseline** (Q6 chain + witness shapes timed per query; suite TOTALs as the backstop; respects ±17% band); moved-shape timing tables on every item |
| E2 | No-plan-movement gate | pin `changed=0` both suites per item, or the moved plan handed to the plan bundle with a roll-up (13 §1 EX-P5) |
| E3 | Alloc budget | object + byte arms neutral-or-better per item (13 §1 EX-P4); witness-shape alloc totals strictly decrease per phase |
| E4 | Values gate | `-digest` + `-diff` clean on every EX1/EX2-seam/EX3/EX4 item (take3 09 §1 R8) |
| E5 | Estimate/plan ratchets untouched | planner A5/B-bars never loosen from executor work; executor items that tighten nothing must move nothing |
| E6 | Directional (NOT acceptance) | Q6-class residual 3.8× → ≤2× serial; spilling shapes off the CPU-bound profile; parallel shapes on the slab path |

Bundle acceptance = E1–E5 per item plus per-phase exits (§§EX0–EX5 above).
E6 is the destination, explicitly excluded from any single item's gate.

---

## Blocked / sequencing notes

- EX0 first: no EX1–EX5 item without protocol + relevant EX0-04 slice
  (13 §8.1).
- EX1 before EX3 geometry (13 §8.2, EX-P7); EX2 audit before elimination
  (13 §8.3); EX1 outputs size EX2-04/EX3-06 (13 §8.4).
- EX5-01 before EX4-04 (13 §8.5); planner P4-01 before EX1 general fix
  (13 §8.6); EX3-07 only if planner P4-05 activates (13 §8.7).
- EX3-01 never gated on Q14/Q3/Q10 (STALE spill witnesses, 12 §13).
- EX3-06 waits on planner P2-11b MCV input; EX4-04 waits on EX5-01
  (executor-owned `Gather` arm) — both wait openly, neither is worked
  around executor-side.
- One variable per commit; every plan-adjacent item its own commit with
  before/after pin + timing + alloc table (13 §8.8).

---

## Progress log

One row per closed phase. Numbers come from the 13 §2.3 artifact header.

| phase | closed | commit range | Q6 serial (goopg / PG) | Q6 parallel (goopg / PG) | witness alloc delta | notes |
|---|---|---|---|---|---|---|
| EX0 | 2026-09-03 | 6ace9567e..e8c7400f9 | 5.48 s / 0.99 s (GOGC=100; take7 3.79 @off non-comparable) | 2.25 s / 0.20 s (GOGC=100; take7 0.84 @off non-comparable) | Q6 1.06 GB serial / ~107 MB parallel-warm (pool-fill hypothesis) | ledger+protocol+worker stats+slices+batches+baseline; TPC-H 252/284 s, TPC-DS 986/997 s; +2 ledger rows (G-EX split b/c, lazy-hash tripwire) |

## Dropped

Items removed from the plan, with the reason and the ledger row. Keep the
original wording — negative results are only legible if they survive
(13 §9; take3 09 §9 form).

| item | date | reason | ledger row |
|---|---|---|---|
| EX1-03 | 2026-09-04 | owner-directed skip: TPC-DS cannot witness (varchar(200) max < 2000 threshold, zero pointers at any SF); synthetic-only value with a silent-corruption risk class on every walk miss; revisit iff a TOAST-heavy corpus appears | take3-EX1-03-dropped |

(End of file)
