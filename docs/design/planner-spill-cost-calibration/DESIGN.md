# Spill-cost calibration — design

Date: 2026-09-06. Branch `plan-narrowing-and-etc`.
Unblocks TODO_ALL **B-13** (`work_mem` BootVal 512 MB → 4 MB),
**B-15** (`btcostestimate` batch) and **E-16** (EX3-03 step-2 resume),
all three of which name "spill-cost calibration" as their single
prerequisite. Method borrowed verbatim from **C-20d**, the item that
turned the same class of problem into a −27% suite result.

This document is a design and a set of probes. It does not claim a
result, and §5 states in advance what a negative outcome looks like so
that one can be recorded rather than argued away.

## 1. Why this is one item and not three

B-13, B-15 and E-16 each stalled for the same reason in different
clothing:

- **B-13** wants PG's real `work_mem` (4 MB against goopg's 512 MB
  BootVal). Lowering it makes far more shapes spill, so every spill
  charge in the model starts deciding plans instead of decorating them.
- **B-15**'s `btcostestimate` batch flipped 14 shapes toward nested loop
  (Q10 5.6×, Q7 ~4×). Index costs are only half of that comparison; the
  other half is what the hash and merge rivals are charged for spilling.
- **E-16** threads session `work_mem` through, is unit-green, and moves
  Q7/Q9 to merge shapes that are *measurably slower than a forced hash*.
  Its own note says the model prices hash above merge while the clock
  says the opposite. That is a spill-charge statement.

So the prerequisite is genuinely shared, and calibrating it once is what
lets all three resume.

## 2. What the spill charge actually is today

Two charges, both in `internal/optimizer/cost_funcs.go`, deliberately
denominated in one currency (`hashsize.EntryBytes`) so that the hash and
sort rivals inside a single `addPath` comparison are commensurable — the
reconciliation M0127-P5.9-k performed, and it should be preserved.

**Hash side** — `hashJoinCost`, guarded by `hashsize.Choose(...).NBatch > 1`:

```go
innerPages := spillPages(in.innerRows, in.innerCols, in.innerAvgVarBytes)
outerPages := spillPages(in.outerRows, in.outerCols, in.outerAvgVarBytes)
startup += cp.seqPageCost * innerPages
run     += cp.seqPageCost * (innerPages + 2*outerPages)
```

**Sort side** — `costSortRun`'s external-merge arm, guarded by
`inputBytes > sortMemBytes`, with `npageaccesses = 2*npages*logRuns`
charged at PG's 3/4-sequential blend.

`spillPages` is `page_size` (costsize.c:6464) with
`relation_byte_size`'s MinimalTuple math replaced by
`hashsize.EntryBytes`.

## 3. Four defects, and they do not point the same way

The reason a single scalar multiplier is *not* the whole answer: the two
charges are biased in **opposite directions**, so one multiplier tuned on
the suite would be fitting the difference between two errors.

### 3.1 The hash charge over-states (known, ledgered)

`spillPages`' own comment says it: the batch **file** encoding is
narrower than the in-memory footprint, because `spillWriter.WriteRow`
frames datums with uvarint lengths instead of storing 48-byte `Datum`
structs, while `EntryBytes` measures the in-memory entry. The comment
calls this "deliberate for now … the safe direction — it deters
spilling", ledgered 2026-08-05 (M0127-P5.7-a). Direction: **over-charge**.

### 3.2 The sort charge fires 4× too late

`costSortRun`'s disk arm triggers at `cp.workMem`, which is
`hashsize.HashMemLimit(DefaultMemLimitBytes=512 MiB,
DefaultHashMemMultiplier=2.0)` = **1 GiB**.

`sortOp` spills at `sortChunkBytes = 256 MiB`
(`internal/executor/operators.go`), a hardcoded chunk threshold that is
not `work_mem` at all.

So every sort whose input lands between 256 MiB and 1 GiB **spills in
reality and is priced as resident**. Direction: **under-charge**, and it
is a trigger error, not a magnitude error — exactly the class that broke
Q14 (a 9-column build the executor never built, pushed 1.5% past the
budget by an otherwise-correct fix). A multiplier cannot repair a
misplaced threshold.

### 3.3 The sort charge assumes zero variable-width bytes

```go
inputBytes := tuples * hashsize.EntryBytes(ncols, 0)
```

`hashJoinCost` passes the real `innerAvgVarBytes`; `costSortRun` passes
**0**, so a sort of text-heavy rows is sized as if every column were
fixed-width. Direction: **under-charge**, compounding §3.2.

This one is nearly free to fix: the sole production caller
(`joinpathsmerge.go:441`) already holds `sub.Rel`, and
`sub.Rel.AvgVarBytes` is the statistic `hashJoinCost` uses. It is a
signature widening, not a new estimate. It must be a **separate commit
with its own arm**, because it changes plans on its own.

### 3.4 `EntryBytes` is load-bearing for geometry, not only for cost

`hashsize.EntryBytes` feeds three consumers and only two of them are
cost:

| consumer | role | may a calibration touch it? |
|---|---|---|
| `hashsize.Choose` in `hashJoinCost` | predicts the executor's `NBatch` | **NO** |
| `spillPages` | bytes written to batch files | yes |
| `costSortRun`'s `inputBytes` | sort spill trigger + page count | yes (trigger separately) |

`Choose` is the *same function* `joinOp.buildGeometry` calls; that
identity is what makes `NBatch > 1` mean "the executor will really
write files". Any multiplier applied inside `EntryBytes` itself would
desynchronise the cost model from the executor — re-creating the exact
defect the D-05 chain spent five measurements locating. **The
calibration must sit in front of the two cost consumers, never inside
the shared byte model.**

## 4. Proposal

Four cuts, separately landable and separately gated, in this order. Each
is its own commit with its own arm; none may be bundled, because each
can move plans by itself.

**Cut 1 — fix the sort's `avgVarBytes` (§3.3).** Widen `costSortRun` to
take `avgVarBytes` and pass `sub.Rel.AvgVarBytes`. No new estimate, no
new constant. Errs high (more bytes ⇒ more spill charge), which is the
same safe direction the hash side already takes.

**Cut 2 — align the sort spill trigger with the executor (§3.2).** The
trigger must be the threshold `sortOp` actually uses. Two candidate
shapes, and the probe in §6.1 decides between them:
 (a) price against `sortChunkBytes` directly, mirroring the
     `Choose`/`buildGeometry` identity the hash side already has;
 (b) make `sortChunkBytes` a function of session `work_mem` so both
     sides read one budget — which is E-16's territory and therefore a
     dependency, not a free choice.
(a) is the smaller change and preserves the "cost mirrors executor"
property; (b) is more PG-faithful. Do not pick before the probe.

**Cut 3 — the calibration proper.** A named multiplier in front of the
spill charge, defaulting to a value chosen by measurement, following
C-20d's shape:

```go
// spillCostMultiplier corrects `spillPages`' known over-statement:
// EntryBytes measures the in-memory entry, spillWriter.WriteRow frames
// datums with uvarint lengths. Ledger M0127-P5.7-a.
const spillCostMultiplierCalibrated = /* measured */
```

Two constants, not one, because §3 shows the two sides are biased
oppositely: `spillCostMultiplier` (hash, expected < 1) and
`sortSpillCostMultiplier` (sort, expected ≥ 1 once Cuts 1–2 land). A
single shared constant would be fitting the difference between two
errors and would not survive either side changing.

**Cut 4 — resume the three blocked items** against the calibrated model,
in their own order (B-13, then B-15, then E-16).

## 5. What a negative result looks like, stated in advance

C-20d's method includes the part that makes it honest: sweep several
values, values-diff every arm, and adopt **the smallest departure from
PG's constants that buys the whole win** — 2 beat 4 there precisely
because 4 bought nothing extra.

The negative outcomes, any of which is a complete deliverable:

- No multiplier value beats 1.0 outside the ±17% per-query band on a
  reproducible pair of runs ⇒ record the measurement, leave the constant
  at 1.0 with the sweep table beside it, and downgrade §4 Cut 3 to a
  ledger row. Cuts 1 and 2 are still correct on fidelity grounds and can
  land regardless.
- A value wins on the suite but moves plan parity AWAY from PG
  (`match` falls) ⇒ that is not adoptable; take3 09 §5 requires the
  monotone direction, and C-20d's calibration is credible precisely
  because parity improved (5/15 → 6/14) alongside the timing.
- A value wins on TPC-H and regresses TPC-DS SF0.5 ⇒ not adoptable.

## 6. Probes required before any cut

**6.1 Does the sort actually spill on this suite? — PARTLY ANSWERED
STATICALLY, 2026-09-06: no, and Cut 2 is therefore inert at the current
`work_mem`.**

`costSortRun` has exactly ONE production caller, `sortPathFor`
(`joinpathsmerge.go:441`), so it prices **merge-join input sorts only**.
Reading the plan capture taken at the E-09a/C-19c/E-11 gate, the TPC-H
suite at SF=1 contains two Merge Joins (Q12 and, nested under Q18's
semi-join, the same shape), and **both take index-ordered inputs**:

```
->  Merge Join  (cost=0.75..1608261.68 rows=6001255 width=168)
      Merge Cond: (orders.o_orderkey = lineitem.l_orderkey)
      ->  Index Scan using orders_pk on orders            (rows=1500000)
      ->  Index Scan using idx_lineitem_orderkey_fkidx on lineitem
```

A startup cost of 0.75 is the tell — there is no Sort beneath either.
So **zero merge-join input sorts exist in the current plan set**, and
§3.2's trigger error, though real, decides nothing on this corpus today.

Consequences, and they re-rank the item:

- **Cut 2 drops behind Cut 1 and Cut 3.** It remains a genuine fidelity
  defect and must still be fixed before B-13 lowers `work_mem` — that is
  precisely when merge shapes with sorts appear — but it cannot be
  measured on TPC-H as it stands, so it must be gated on a forced shape
  or on the reduced-`work_mem` arm, never on the suite total.
- The dynamic half of this probe is still required, at the **E-16
  session `work_mem` values**, where the merge shapes E-16 complains
  about do appear. That is where Cut 2 becomes measurable.
- The one-caller fact also means Cut 1 (§3.3, `avgVarBytes=0`) has a
  correspondingly small blast radius — one call site, merge-join sorts
  only — which makes it cheap to land and cheap to revert.

**6.1b — the larger finding this probe turned up.** Q18's top-level
`Sort (rows=1565307 width=204)`, the biggest sort in the suite and part
of the suite's slowest query, is **not priced by any cost function at
all**: goopg has no upper-rel `PathSort`, so `costSortRun` never sees it.
That is TODO_ALL **C-12**'s subject, not this item's, and it is recorded
here because it means "goopg's sort cost model" currently describes a
strictly smaller thing than it appears to. Any spill calibration reasoned
from suite-wide sort behaviour would be reasoning about nodes the model
does not price. Referred to the C-11/C-12/C-13 design.

**6.2 Measure the real batch-file bytes per row — ANSWERED
ANALYTICALLY, 2026-09-06, and it refutes the scalar-multiplier plan.**

The encoder is fully determined, so the magnitude §3.1 left open can be
derived rather than measured. From `internal/executor/spill.go`
(`WriteRowHashed` → `appendRowPayload` → `writeFrame` → `encodeDatum`),
one spilled row on disk is:

```
4  frame length (LittleEndian uint32, writeFrame)
4  join hash value (WriteRowHashed only)
1  column count (uvarint, ncols < 128)
   per column: 1 kind byte, then
        KindNull   0                KindBool  1
        KindInt    8                KindTime  8 (+ subtype)
        KindString 4 + len          KindBytes 4 + len
```

Against `hashsize.EntryBytes(ncols, avgVarBytes) = 48*ncols + 24 +
avgVarBytes` (`DatumBytes = 48`, `RowSliceBytes = 24`).

Worked points, hashed writer, `v` = total variable-width payload bytes:

| shape | `EntryBytes` | on disk | over-statement |
|---|---|---|---|
| n=2, v=0 — the Q9 `orders` build measured at 120.0 B/row | 120 | 27 | **4.4×** |
| n=5, v=0 | 264 | 54 | 4.9× |
| n=9, v=0 — the Q14 phantom build | 456 | 90 | 5.1× |
| n=5, v=100 (2 text cols) | 364 | 154 | 2.4× |
| n=2, v=400 (wide text) | 520 | 418 | 1.2× |

**The ratio is not a constant — it runs from ~5× down to ~1.2×**, because
the fixed 48 B/column of the in-memory `Datum` is pure overhead on disk
while variable-width payload is carried at par. A scalar
`spillCostMultiplier` would therefore be fitting one corpus's average
column mix, and would drift the moment the plan set changed. That is the
same failure mode as pinning a cost test to a literal instead of its
constant.

**Cut 3 is re-specified accordingly**: introduce a second byte model
beside `EntryBytes` — call it `hashsize.SpillBytes(ncols, avgVarBytes)`
— that transcribes the encoder above, and have `spillPages` call it
instead of `EntryBytes`. Properties this buys that a multiplier cannot:

- it is *derived*, not fitted, so it needs no suite sweep to justify its
  value and cannot be over-fitted to TPC-H;
- it is shape-correct across the whole 1.2×–5× range rather than at one
  average;
- it keeps §3.4 intact — `hashsize.Choose` still calls `EntryBytes` and
  still mirrors `joinOp.buildGeometry`, so `NBatch > 1` keeps meaning
  "the executor will really spill";
- and it makes `spill.go`'s encoder and `hashsize`'s model an
  **explicit sibling pair** that must change together, which is this
  codebase's most reliable silent-bug class (encode/decode,
  fast-path/interpreted, column-lookup/star-expansion). Pin them with an
  agreement test that encodes real rows and compares actual byte length
  against the model, exactly as the D-01 descriptor test pins two
  transcriptions of `pg_type.dat` against each other.

The suite sweep does not disappear; its role changes from *discovering* a
value to *confirming* that the derived model does not regress the suite,
and a `spillCostMultiplier` may still be added on top if the derived
model proves systematically off. But it starts at 1.0 with a reason.

Two residual unknowns the analysis cannot settle, both to be checked on a
real batch file when the bench frees up: the `bufio` writer's 8 KiB
buffer and filesystem block rounding add a per-file, not per-row, term
that matters only for many small batches; and `KindTime`'s subtype byte
and any arena-backed kinds not listed above need confirming against
`encodeDatum`'s full switch.

**6.3 Confirm both candidates exist.** For any query whose plan is
expected to move, instrument `addPath` to prove the hash and merge paths
were BOTH generated at that parameterisation before comparing costs. A
past investigation burned five hypotheses on Q8 because the index
producer emitted nothing there and the costs were never the question.

## 7. Interaction with the parallel work — read before measuring

goopg's cost model has no parallel dimension: `MaybeAddGather` is a
post-planning size rule and `drivingScan` admits only a hash join under a
Gather, so **any term that makes a hash join dearer can silently cost a
5-worker Gather**. Three individually-correct cost fixes each regressed
TPC-H 10–22% this way (Q5 +444%, Q10 +221%, Q9 +115%).

`spillCostMultiplier < 1` makes the hash side *cheaper*, so it points
away from that trap — but `sortSpillCostMultiplier ≥ 1` makes the merge
rival dearer, which has the same sign as the trap in reverse and could
push shapes toward hash for the wrong reason. Q5, Q9 and Q10 are the
canaries either way and must be in every arm.

**C-19d (priced `PathGather`/`PathGatherMerge`) is in flight now.** If it
lands before Cut 3 is measured, measure on top of it: a calibration
fitted to a parallel-blind model would have to be redone. If it does not
land, Cut 3's arms must report Q5/Q9/Q10 individually and state the
parallel-blindness caveat.

## 8. Gates

Per cut, non-negotiable:

- TPC-H SF=1: `tpch-runner -digest` + `-diff`, **24 MATCH by value**,
  never row counts (a row-count gate held all 21 result sets
  byte-identical while Q2 went 43× slower).
- **Time every query whose plan changed**, not just the total.
- Plan capture + `scripts/pg-plan-parity-diff.py` roll-up, reporting
  `match`/`shapediff` before and after; `make plan-gate` in structural
  and `MODE=costs`.
- TPC-DS SF0.5 sweep: `PASS=95 MISMATCH=0 CKMISMATCH=0`.
- Fresh memory-capped server per arm (`scripts/tpch-acceptance-arm.sh`),
  `GOGC=100 GOMEMLIMIT=12GiB`, `GOOPG_ANALYZE_SEED` pinned, autovacuum
  off. Server age held constant across arms — a server that has just run
  a heavy query sits at GOMEMLIMIT with GOGC=off and thrashes GC, which
  mimics a regression.
- Cost tests expressed through the named constant, never its literal
  value. Pinning the literal is what hid an unapplied calibration worth
  27% of the suite for an entire release cycle.

## 9. Files

`internal/optimizer/cost_funcs.go` (`costSortRun`, `spillPages`,
`hashJoinCost`, `costParams`), `internal/optimizer/joinpathsmerge.go`
(the one `costSortRun` call site), `internal/executor/operators.go`
(`sortChunkBytes`, read-only unless Cut 2 takes shape (b)),
`internal/executor/hashsize/hashsize.go` (read-only — see §3.4).
