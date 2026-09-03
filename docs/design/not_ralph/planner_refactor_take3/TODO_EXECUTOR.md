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
- [~] EX0-02 Commit the measurement protocol — design `docs/design/executor-ex0-02-protocol/DESIGN.md` (reviewed, 5 blocking findings fixed); conforming artifact `EX0-02-q6-serial-scold` still to run before close.
  *design: 13 §2.2–2.3; gate: protocol doc + one conforming artifact.*
- [ ] EX0-03 Surface per-worker hash/sort counters in EXPLAIN ANALYZE —
      thread existing worker counts to the leader (PG shows them; 11 §10
      notes they die with the worker). Counting already happens; plumbing
      + golden test only.
      *design: 13 §2.2; gate: golden test, plans byte-identical, no timing
      claim.*
- [ ] EX0-04 Per-operator timing harness — repeatable breakdown (scan
      decode / prefilter / clone / join-probe / sort-compare / spill write)
      on Q6-class, Q9-class, Q4/Q7/Q13-class shapes. Validate against the
      known Q6 prefilter share before trusting new slices.
      *design: 13 §2.2; gate: slices published; pin `changed=0`.*
- [ ] EX0-05 Batch/width counters — EXPLAIN-visible batch reporting for
      hash builds + recorded narrowed widths (P4-01 witness form: Q9
      `Batches:` at 64 MB S-cold, narrowed width ≈100 not 6).
      *design: 13 §2.2; gate: counters report on witnesses; pin
      `changed=0`.*
- [ ] EX0-06 Re-baseline the Q6 chain end-to-end under the protocol —
      serial 3.792 s / parallel 0.838 s (take7) re-measured with timing +
      alloc arms + headers — and record per-query timing+alloc baselines
      on both suites (TPC-H SF=1 + TPC-DS SF0.5, same protocol). The Q6
      chain is the executor baseline every later item diffs against; the
      per-query tables are E1's denominator.
      *design: 13 §2.3; gate: conforming artifact (Q6 chain + per-query
      tables both suites), no behaviour change.*

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

- [ ] EX1-01 Scan projection pushdown — `seqScanOp` decodes only target-list
      columns; extend to index-only/batch paths as they gain targets.
      *design: 13 §3; gate: values-diff both suites + pin + alloc arm.*
- [ ] EX1-02 Deform-some-attributes — generalise take5's 6-of-16 into the
      decode path: stop at the highest referenced attribute per consumer
      (`slot_getsomeattrs` analogue, 10 §3). Whole-row deform survives only
      on `WHOLEROW`/all-keys paths.
      *design: 13 §3; gate: EX0-04 decode slice down on witnesses; values +
      pin.*
- [ ] EX1-03 Lazy detoast — per-attribute resolution on first use instead
      of whole-row `DetoastRow` at scan time (11 §14). Witness: TOAST-heavy
      TPC-DS shapes, not Q6.
      *design: 13 §3; gate: TOAST contract tests per type; values + pin.*
- [ ] EX1-04 Owned-row narrowing — shrink `MaterializedSlot` payloads to
      referenced columns where retention allows (P4-A "drop seven columns").
      After EX1-01/02 so boundaries are audited on narrow rows.
      *design: 13 §3; gate: Q9 narrowed width recorded; values + pin +
      alloc arm.*

Landed narrow fix (record, not work):

- [x] EX1-00 Copy-after-filter + 6-of-16 prefilter deform (take5) —
      11.51 → 6.63 s; `cloneRowOwned` 26.6% → 1.16% CPU; evidence:
      `benchmark-results-take5.md:20-32,276-277` (11 §17 row 3).

---

## EX2 — Clone elimination (copy only at retention boundaries)

*Design: 13 §4. Gates: 13 §2.3 + alloc arm (primary) + pin.*
Exit: alloc arm down on witnesses, timing neutral-or-better, per-seam
tripwires green, values clean, plans unchanged.

- [ ] EX2-01 Retention-boundary audit — enumerate every `cloneRowOwned` /
      `MaterializeArena` / `acquireRow` site with its retention reason.
      Document only, no behaviour change; later items execute against it.
      *design: 13 §4; gate: audit doc reviewed, no code.*
- [ ] EX2-02a Ownership passing at join seams — sole-owner bounded-lifetime
      clones become transfers. Join seams first (G-EX3 cascade product
      shrinks here too).
      *design: 13 §4; gate: alloc arm down; seam tripwire tests; values +
      pin.*
- [ ] EX2-02b Ownership passing at agg input — sole-owner
      bounded-lifetime clones become transfers. Separate commit from 02a
      (one seam family per commit, EX-P3).
      *design: 13 §4; gate: alloc arm; seam tripwire tests; values +
      pin.*
- [ ] EX2-02c Ownership passing at gather transfer (= 13 EX2-04) —
      workers stream materialised rows today
      (`operators_gather.go:334-336`); move to ownership transfer across
      the queue under arena rules (`parallel_runtime.go:31-71`); serial
      control arm mandatory. Separate commit from 02b (EX-P3).
      *design: 13 §4; gate: alloc arm; values + pin + serial arm.*
- [ ] EX2-03 Pool sizing — tune `acquireRow` widths/return discipline
      against EX0-04 slices, after the audit. Measure, do not guess.
      *design: 13 §4; gate: pool-hit + alloc + timing arms; pin.*

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

- [ ] EX3-01 Verify the `runtime.Stack` elimination, then price the
      remaining spill cost — elimination LANDED (`1d6b1e396`;
      `spill.go:78-85,121-135,181`); re-measure the superseded 69–86% /
      3.3–7.3× projections on Q4/Q7/Q13-class shapes to close the
      Stack-walk claim, then price per-row WaitEvent instrumentation +
      encode + file I/O with a reader-path audit; spill accounting moves
      in the same commit.
      *design: 13 §5; gate: top-slice re-measured on spilling shapes;
      counters + pin + alloc arm. NOT gated on Q14/Q3/Q10 (STALE
      witnesses, 12 §13).*
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
- [ ] EX3-05 Sort-compare fast path — close `lessRows` / CTID-tail cost
      (q16 34%) on the decoded-vector keys. Pure-timing gate; no plan can
      move. Do first if EX3-01 stalls.
      *design: 13 §5; gate: sort-compare slice down; pin (trivially holds).*
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

## Dropped

Items removed from the plan, with the reason and the ledger row. Keep the
original wording — negative results are only legible if they survive
(13 §9; take3 09 §9 form).

| item | date | reason | ledger row |
|---|---|---|---|

(End of file)
