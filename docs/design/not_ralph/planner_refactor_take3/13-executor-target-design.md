# 13 — Executor target design (take3 synthesis, executor bundle)

How goopg gets from "structurally complete, allocation-immature" to
"executor-side OLAP parity after plan parity" — phased, measured,
allocator-aware. Read [12-executor-gap-analysis.md](12-executor-gap-analysis.md)
for the evidence this design responds to. Take3
[09-verification-and-acceptance.md](09-verification-and-acceptance.md) is the
measurement contract both bundles share; this document adds only the
executor-specific instruments and gates.

## 0. Thesis

**Make the per-row cost proportional to the columns the query touches, copy
only at retention boundaries, batch spill like the semantics require, compile
every hot expression site, and run the same compiled path on workers.**

Take5–take7 proved the thesis is reachable in slices: each halved Q6
(23.40 → 11.51 → 6.63 → 4.49 → 3.79 s serial, 12 §§1.2, 11). Each slice did
one thing: decode less, copy later, copy once, box never, interpret once.
The phases below continue the same motion in leverage order. Nothing here
changes which plan any query gets — that is the plan bundle's property, and
this bundle asserts it per item (§1, plan-shape pins).

### 0.1 Non-goals

- **Plan parity itself.** Bars A1–A5 (take3 09 §4.1) belong to take3 01–09.
  This bundle consumes the plan-parity instrument (09 §2.2) as a pin, not as
  an objective.
- **Time parity on any schedule.** B4-style engine-time targets (take3
  09 §4.2) are directional only: plan parity plus this bundle approaches
  them, neither promises them.
- **A new row representation.** No Datum re-layout below 48 B, no
  columnarisation, no vectorised engine. The win is touching less of the
  row, not rebuilding it (12 §12: narrowing without shrink still halved
  batches; shrink without narrowing is not proposed).
- **JIT/LLVM.** The target is compiled-slab coverage per operator (EX4),
  not a JIT provider. PG's JIT substitution point (10 §12) stays a
  documented non-counterpart.

---

## 1. Design principles

Constraints, not preferences. Each was paid for (take2 §4.3 via 07 §5, or
the take5–take7 chain via 11 §17).

**EX-P1 — PG semantics are the specification, including batch-file
discipline.** Where PG batches (hash batch pairs re-reading both sides),
runs (logtape merge), or pins (skew keys resident), goopg reproduces the
discipline (10 §§6, 9, 11). A faster spill that changes which rows spill is
a failure even when it is faster. Deviations require a committed measurement
and a ledger row (take3 09 §4.3 C7).

**EX-P2 — No query-specific forcing.** Established for the planner by
`cost-model/15` and M0126 (07 §5); binding here by the same logic: no
TPC-H/Q6-shaped fast path, no per-query prefetch depth, no operator that
detects a benchmark. EX4 extends the prefilter *approach* per operator; any
item whose gate names a single query is split or dropped.

**EX-P3 — One variable per commit.** M0126's lesson via 07 §5: an item that
moves allocator and algorithm together is two items. Sibling rule from
08 §1 P5, carried: cancelling pairs (Stack-tax removal + spill accounting
that reports through the same counters) move in *one* commit — one
variable, both halves.

**EX-P4 — Allocator and time measured together.** A CPU win that doubles
allocations is not a win. Every item's gate carries both a timing arm and
an allocation arm (object counts + bytes on the profiled shape); either arm
regressing beyond its band fails the item (gates in `TODO_EXECUTOR.md`).

**EX-P5 — Executor changes must not move plans.** The plan-shape pin: every
item runs the plan-parity instrument (take3 09 §2.2) before/after on both
suites and asserts `changed=0` (or names the moved plan and hands it to the
plan bundle with a before/after roll-up). Projection-adjacent work (EX1)
additionally gates on values (`-digest` + `-diff`, take3 09 §1 R8) — never
counts alone (P4-01b faster-and-wrong, 12 §12).

**EX-P6 — Both suites timed, serial control arm always.** Take3 09 §1
R1/R2/R6/R7 bind here unchanged: TPC-H SF=1 and TPC-DS SF0.5, fresh server
per arm, per-query attribution (plan changed?) plus TOTAL arm. Parallel-path
items additionally carry a serial control arm (`--serial` analogue, take3
09 §2.1): the serial shape must not move when the parallel path changes.

**EX-P7 — Narrowing before batching math.** `FINDING-p401-alone-is-not-enough`
(12 §12): batch counts are computed over narrowed widths, so EX3's geometry
work starts from EX1's outputs. Any batching item attempted on pre-EX1
widths is premature by rule, not by taste (§8).

---

## 2. EX0 — Instruments (first, before anything else changes)

Nothing in EX1–EX5 is falsifiable without per-operator attribution. EX0
builds it; EX0 moves no executor behaviour (`changed=0` on plans, timing
inside noise — the Phase-0 rule from 08 §3, applied here).

### 2.1 What already exists (do not rebuild)

- **EXPLAIN ANALYZE with per-node timing/rows** — the analogue of PG's
  `ExecProcNodeInstr` (10 §1) exists on the serial path. Known hole: worker
  hash counters die with the worker, so parallel hash stats cannot surface
  as PG's do (11 §10). EX0-03 closes the surfacing, not the counting.
- **Plan-parity capture/diff** (take3 09 §2.2, built by planner P0-05/06/07)
  — reused verbatim as the plan-shape pin (EX-P5). If the planner bundle has
  not landed it yet, executor items pin against goopg-vs-goopg capture
  (`make plan-gate` + TPC-DS `plans` channel, take3 09 §2.1) instead, and say
  so on the item line.
- **The take6/round5 methodology** — alternating-A/B with a byte-identical
  result gate, `perf` flamegraphs, allocation profiles
  (`perf-optimize-take6/RESULTS.md`, round5 spill design, take7 results §7).
  EX0-02 writes it down as the mandatory protocol (§2.3) rather than
  re-deriving it per item.

### 2.2 What EX0 builds

- **EX0-01 Executor backlog ledger.** One `.ralph/deferral_ledger.md` row
  per open 12 gap (§§3–10) with the 10 § upstream citation EX-P1 requires.
  (The take2 P7-03 rows are absent under `take2-executor*`; this replaces
  the grep with named rows.)
- **EX0-02 Measurement protocol.** The artifact header (take3 09 §6:
  label, date, commits, ports, suite, regime, flags line, host load) plus
  the executor additions: GOGC/GOMEMLIMIT, `work_mem` **and**
  `effective_cache_size` per arm (07 §7.1), alloc arm (profiler + object
  counts), and the flamegraph/perf invocation from take6/round5 with its
  symbol list. Protocol violation voids the item's numbers.
- **EX0-03 Worker stat surfacing.** Thread per-worker hash/sort counters to
  the leader for EXPLAIN ANALYZE (PG shows them; 11 §10 notes they die with
  the worker). Counting already happens per worker; this is plumbing plus a
  golden test. No timing claim.
- **EX0-04 Per-operator timing harness.** A repeatable per-operator breakdown
  (scan decode / prefilter / clone / join-probe / sort-compare / spill
  write, at minimum) on the witness shapes (Q6-class scan-filter-agg,
  Q9-class multi-join cascade, Q4/Q7/Q13-class spill), so each EX1–EX4 item
  can name its target slice *before* changing it. Reuses EXPLAIN ANALYZE
  buffers where they reach; profiles where they do not. Decides G-EX6's
  type-by-type remainder list empirically.
- **EX0-05 Batch/width counters.** EXPLAIN-visible `Batches:`-class
  reporting for hash builds at the witness shapes plus recorded narrowed
  widths — the analogue of the P4-01 witness gate (take3 09 §5 P4 row:
  Q9 `Batches:` 8→1 at 64 MB S-cold, narrowed width ≈100 not 6). EX3 items
  gate on these counters, not only on time.

### 2.3 Protocol (mandatory, executor reading of take3 09 §6)

Fresh server per arm through the cgroup cap; hold server age constant
(sweep-tail collapse, 07 §7.3); reap orphans; one benchmark at a time;
throwaway ports 5533/5534. Timing regime per suite as in 12 §1.3. Noise
band ±17%; per-query claims above 1.2× or on repeats; suite claims on
totals; TOTAL arm read with plan attribution (take3 09 §1 R6/R7).
**Attribution rule** (R7): a runtime move is yours only if the plan is
unchanged (pin) and the EX0-04 slice moved. **Values rule** (R8):
projection/join-adjacent changes gate on `-digest` + `-diff` values, never
counts.

### EX0 exit

Ledger rows filed; protocol committed and used once end-to-end (re-baseline
the Q6 chain 3.792 s serial / 0.838 s parallel as the executor baseline);
worker stats surface; per-operator slices published for the three witness
shapes; batch counters report on Q9. No plan moved, no timing claimed.

---

## 3. EX1 — Tuple narrowing (touch less of the row)

Target: the per-row cost proportional to referenced columns. Depends on
planner P4-01 (PathTarget) for the *general* fix — EX1 consumes narrowed
target lists; it does not invent its own pruning (12 §12, P4-01b lesson).

- **EX1-01 Scan projection pushdown / column pruning at scan.** `seqScanOp`
  (and index-only/batch paths as they gain targets) decodes only columns in
  the plan's target list. Takes the target list as input; gate includes the
  values-diff (R8) on both suites.
- **EX1-02 Deform-some-attributes.** Generalise the take5 narrow fix
  (6-of-16 on the prefilter path) into the decode path: stop at the highest
  referenced attribute per consumer — the `slot_getsomeattrs` analogue
  (10 §3). Whole-row deform survives only on `WHOLEROW`/all-keys paths.
- **EX1-03 Lazy detoast.** Resolve `KindToastPointer` per attribute on
  first use instead of whole-row `DetoastRow` at scan time (11 §14; PG:
  10 §3 deform laziness covers detoast placement). Wide-column queries
  (TOAST-heavy TPC-DS shapes) are the witness, not Q6.
- **EX1-04 Owned-row narrowing.** Shrink owned rows (`MaterializedSlot`
  payload) to referenced columns where retention allows — the executor half
  of the P4-A correction ("drop seven columns", 07 §3.2). Sequenced after
  EX1-01/02 so boundaries are audited on narrow rows (EX2's input).

### EX1 exit

Per-operator slices show decode/detoast proportional to referenced columns
on the witness shapes; Q9 narrowed width recorded (≈100, not 6 — the P4-01
witness form); values-diff clean both suites; plans unchanged
(plan-shape pin). Batching math still uses old geometry — that is EX3's
item, explicitly not this phase's.

---

## 4. EX2 — Clone elimination (copy only at retention boundaries)

Target: the 36–41% object share (11 §17 row 9). Method: audit, then
eliminate with proof — never by inspection (use-after-reset class, 10 §13).

- **EX2-01 Retention-boundary audit.** Enumerate every `cloneRowOwned` /
  `MaterializeArena` / `acquireRow` call site with its retention reason
  (which `Next()` call the row outlives, which reset would free it).
  Document, no behaviour change. This is the map EX2-02… execute against.
- **EX2-02a Ownership passing at join seams.** Where the audit shows a
  clone whose consumer is the sole owner for a bounded lifetime, pass
  ownership instead of cloning (the G-EX3 cascade product shrinks here
  too). Join seams first, one commit, with alloc-arm gate.
- **EX2-02b Ownership passing at agg input.** Same sole-owner transfer
  for aggregate-input clones. Separate commit from 02a (one seam family
  per commit, EX-P3), with alloc-arm gate. (Gather transfer is its own
  commit, EX2-04 / TODO EX2-02c, under the arena rules.)
- **EX2-03 Pool sizing.** Tune `acquireRow` pooled widths (≤64 today) and
  return discipline against the EX0-04 slices: pool hits are load-bearing
  only where the audit keeps allocations. Measure, do not guess — pool
  tuning without the audit is benchmark-tuning (EX-P2).
- **EX2-04 Gather transfer without re-materialisation (TODO EX2-02c).**
  Workers stream
  materialised rows today (`operators_gather.go:334-336`); move to
  ownership transfer across the queue with the arena rules
  (`parallel_runtime.go:31-71`) as the safety contract. Serial control arm
  mandatory (EX-P6).

### EX2 exit

Alloc arm down on the witness shapes with timing neutral-or-better; no
retention failure (use-after-reset tripwire tests per seam); plans
unchanged; values-diff clean.

---

## 5. EX3 — Spill, sort, and hash batching (degrade into sequential re-reads)

Target: spilling shapes stop being CPU-bound on introspection and start
behaving like PG's run/batch discipline (10 §§6, 9, 11). Sequenced after
EX1 by EX-P7 (batch counts are computed over narrowed widths).

- **EX3-01 Verify the `runtime.Stack` elimination, then price the
  remaining spill cost.** The round5 Stack-tax elimination has LANDED
  (`1d6b1e396`: constructor-cached handle, `spill.go:78-85,121-135,181`
  — no per-row Stack walk remains), so this item no longer lands that
  design: it re-measures the superseded 69–86% / 3.3–7.3× projections
  on Q4/Q7/Q13-class spilling shapes to close the Stack-walk claim,
  then prices the remaining per-row cost (cached-handle
  `WaitEventStart/End`, per-column `encodeDatum`, bufio/file I/O) with
  a reader-path audit. Spill accounting moves in the same commit
  (EX-P3 sibling rule).
- **EX3-02 Dense-chunk build rows.** Pack spilled/retained build rows
  contiguously (the `dense_alloc` analogue, 10 §6: bump-allocate per chunk,
  dedicated chunks for oversized tuples) replacing per-row allocations on
  the build path. One `palloc`-class call per chunk instead of per tuple.
- **EX3-03 Batch-file discipline matching PG semantics.** Symmetric
  probe-side spill for later batches; batch pairs re-read both sides; peak
  near `work_mem` (10 §6). Measured at both `work_mem` budgets (12 §6:
  BootVal note) with the EX0-05 batch counters as gate, not only time.
- **EX3-04 Sort spill runs + merge discipline.** Run formation on
  `flushChunk` and tape-style merge-back (the logtape analogue, 10 §9)
  replacing whole-chunk spill. Sort-compare speed (`lessRows`,
  abbreviated-key analogue) is a separate item (EX3-05): one variable per
  commit (EX-P3).
- **EX3-05 Sort-compare fast path.** Decoded-vector keys already exist
  (`sortKeyVals`); close the row-fallback (`lessRows`, q16 34%) and the
  CTID-tail cost. Pure-timing gate — no plan can move, which makes this the
  safest EX3 item; do it first if EX3-01 stalls.
- **EX3-06 Skew residency + single-pass build.** MCV-pinned hot keys that
  bypass batching (10 §6 skew table) once planner P2-11b delivers the MCV
  list to the cost site; collapse the two-pass/two-map build toward single
  retention (11 §17 row 14). Last in EX3: needs planner MCV input *and*
  EX1-narrowed rows to be measurable.

### EX3 exit

Spilling shapes show sequential-I/O-dominated profiles (no per-row
introspection in the top slices); batch counters match PG semantics on the
witnesses at both budgets; sort-compare share down on q16-class shapes;
values-diff clean; plans unchanged.

---

## 6. EX4 — Expression fast path (compile every hot site)

Target: extend the take7 prefilter approach per operator — same slab, same
`ExprAdapter` fallback contract — with no query-specific forcing (EX-P2).

- **EX4-01 `filterOp` compilation (serial).** Compile the `Filter` above
  the scan instead of re-interpreting the prefilter's predicate
  (`operators.go:565`; ~0.33 pp per take7 §7). Serial path first: no
  planner-shape dependency. Gate: the double-evaluation disappears from the
  EX0-04 slice.
- **EX4-02 Join residual + key compilation.** Compile merge residuals
  (`mergeResidualMatch`) and join-key evaluators per `plan.Algo` arm
  through the same slab. Values-diff mandatory (join-adjacent, R8).
- **EX4-03 Agg transition fast path.** Per-row `transfn` evaluation is the
  OLAP-critical agg cost (10 §8: per-row transfn + per-group state size
  dominate). Compile the transition expressions for the builtin aggs;
  `MIXED`/spill behaviour unchanged.
- **EX4-04 `Gather`-arm slab reachability (blocked on EX5-01).**
  Compiling `filterOp` on the parallel path needs the `Gather` arm in
  `buildRec` (11 §§1, 12) — executor-owned work delivered by EX5-01
  (`Build` already handles `Gather` at `executor.go:264-279`, with the
  `Join`-arm `:604-616` / default→legacy-adapter `:618-626` pattern to
  follow), so this item waits on EX5-01, not on the planner bundle. The
  plan-shape pin is the planner interface, not the work item. If EX5-01
  is unlanded, EX4-04 waits; EX4-01…03 do not.

### EX4 exit

EX0-04 shows no interpreted hot-site slice on the witness shapes (serial;
parallel after EX4-04); per-site 1.1–1.2×-class wins compose without alloc
regressions; plans unchanged; values-diff clean.

---

## 7. EX5 — Parallel executor (same compiled path, more workers)

Target: parallel queries run the slab builder with shared builds and
ordered exchange — PG's shape (10 §10), not a second executor. Planner-side
parallelism (partial paths, `cost_gather`, P5-01…P5-08) stays in the plan
bundle (07 §3.6, 08 §8); EX5 is mechanics only.

- **EX5-01 Slab parity for `Gather`.** Give `buildRec` its `Gather` arm so
  parallel queries stop falling back to legacy `Build` (11 §1). This
  unlocks EX4-04 and re-proves every EX1–EX4 win on workers. Serial control
  arm mandatory.
- **EX5-02 Shared build hardening.** Harden `sharedHashBuild` /
  `parallelBuildLazyHashTable` (11 §10) for the now-widened parallel
  coverage: barrier discipline for build/probe phases (PG: 10 §10),
  cooperative-stall measurement under skew, worker-count scaling on the
  Q9-class shape.
- **EX5-03 Gather/GatherMerge ordering + exchange.** Ordered exchange with
  worker-sorted slices and leader heap merge (the `SharedTuplestore`
  analogue discipline, 10 §10); `Gather Merge → Sort → Parallel scan`
  stays cost-decided with the planner bundle (P5-05; permitted-divergence
  candidate with the q16/q10/q13 measurement, take3 09 §4.4 case 1).
- **EX5-04 AIO `ReadStream` decision.** The type exists unwired (11 §14,
  §18 item 2). EX5-04 is a measurement item with two legal outcomes: wire
  it behind the scan path with a depth policy and a timing+alloc gate, or
  ledger-decline it (pool hints + worker parallelism suffice). Not a
  commitment (12 §8).

### EX5 exit

Parallel Q6-class shapes run the slab path with shared builds; worker stats
surface (EX0-03) on parallel EXPLAIN ANALYZE; serial arms unchanged;
`parallelism` diffs belong to the planner bundle and are only pinned here.

---

## 8. Sequencing constraints (explicit)

1. **EX0 first.** No EX1–EX5 item starts before EX0-02 (protocol) and the
   relevant EX0-04 slice exist. Unmeasurable work is undeferrable work only
   after it is measurable.
2. **EX1 before EX3's geometry.** Batch counts, arena sizing, and spill
   thresholds are computed over narrowed widths (EX-P7). EX3-02/03/06 on
   pre-EX1 widths are premature by rule.
3. **EX2 audit before EX2 elimination.** EX2-01 maps; EX2-02… execute. Pool
   sizing (EX2-03) after the audit, never before.
4. **EX1 before EX2-04 / EX3-06 scale arguments.** Ownership-transfer and
   skew-residency designs quantify over narrowed rows; write them against
   EX1 outputs.
5. **EX5-01 before EX4-04.** The executor-owned `Gather` slab arm in
   `buildRec` (11 §§1, 12) is the precondition for parallel expression
   compilation; EX4-04 is BLOCKED on EX5-01. The plan-shape pin is the
   planner interface, not the work item. Serial EX4 items proceed
   independently.
6. **Planner P4-01 before EX1 general fix.** Executor narrowing consumes the
   plan's target lists (12 §12). The take5-narrow pattern may extend to new
   sites meanwhile, but general deform-narrowing keys off P4-01 outputs.
7. **P4-05 (Incremental Sort) dependency reversed.** The planner's
   incremental-sort path is BLOCKED on executor support (11 §9); the
   executor presorted-prefix input contract is therefore EX3-adjacent work
   the planner bundle waits on — file it as EX3-07 if P4-05 activates,
   do not silently absorb it. Tiebreaker: the executor publishes the
   input contract first (as EX3-04-adjacent work or a spike), before and
   independently of planner P4-05 — or P4-05 prototypes behind a flag.
   Either ordering works; the two sides waiting on each other does not.
8. **One variable per commit** (EX-P3); every plan-adjacent item its own
   commit with before/after pin + timing + alloc table (`TODO_EXECUTOR.md`
   close-line format).

---

## 9. Risk register

| phase | risk | evidence | mitigation |
|---|---|---|---|
| EX0 | protocol measures the labeller (take3 09 §1 #3 precedent) | bitmap census read 0 while winning | EX0-04 validated against known slices (Q6 prefilter share) before trusting new ones |
| EX1 | narrowing faster-and-wrong | P4-01b Q2/Q5 0-rows, Q18 wrong tuples (12 §12) | target lists from the plan only; values-diff (R8) on both suites; never executor-side pruning |
| EX1 | detoast laziness changes error/NULL timing | TOAST contract (`toast.go:213`) | contract tests per type; values-diff, not counts |
| EX2 | use-after-reset corruption | the classic class (10 §13) | per-seam tripwire tests; audit-first rule (§8.3); releaseRow discipline reviewed per item |
| EX2 | ownership passing aliases across `Next()` | VirtualSlot compose sites (11 §3) | sole-owner proof per seam in the item; fuzz the cascade shapes |
| EX3 | Stack-tax removal changes spill accounting | counters report through the same path | cancelling pair in one commit (EX-P3); EX0-05 counters gate alongside time |
| EX3 | batch discipline changes *which* rows spill | PG semantics are the spec (EX-P1) | batch-counter equality + values-diff; two budgets measured |
| EX3 | skew work without MCV input is benchmark-tuning | needs P2-11b first (12 §9) | EX3-06 last; planner input named as precondition, not assumed |
| EX4 | compiled site diverges from interpreter | `ExprAdapter` fallback contract; bounds-check parity load-bearing (11 §12) | twin-parity tests per site (`join_compiled_key_test.go:6-20` pattern); values-diff |
| EX4 | query-specific forcing creeps in | EX-P2 | gate names operators, never queries; review checks the item title |
| EX5 | parallel path diverges from serial results | arena-transfer rules (11 §10) | serial control arm (EX-P6); Datum-safety tests (`parallel_substrate_test.go:26-80` pattern) |
| all | sweep variance read as regression | byte-identical "regressions" (take3 09 §1 #7) | attribution rule (plan unchanged + slice moved) |
| all | CPU win doubles allocs | EX-P4 | alloc arm fails the item independently |
| all | executor item moves a plan | plan-shape pin (EX-P5) | pin first; moved plan handed to plan bundle with roll-up, never fixed executor-side by preference |

---

## 10. Out of scope (with pointers, not promises)

Planner P4-01/P2-11b/P5-01…P5-08 and the plan-parity instrument stay in
take3 01–09 (12 §1.3). TOAST statistics (planner P1-11), expression-index
stats (P1-10), and extended statistics (P1-22/23/24) are planner inputs EX3
consumes, not executor work. Uncancellable probe loops (measurement hazard,
07 §6) get cancel points only if an EX3/EX5 item already owns the loop —
no standalone cancellation project. `Datum` re-layout below 48 B and JIT
are declined in §0.1 with reasons; re-proposing either requires new
measurement, not new argument.

(End of file)
