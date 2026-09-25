# R6 results — the window's Sort is now in the plan

*Round 6 of `../TODO.md`. Design: `DESIGN.md` (committed `fbbe55a4b`).
Implemented, gated and measured 2026-09-08.*

## 1. Verdict

Slice (A) landed exactly as designed, and **every advance prediction
held**, including the negative one.

| | before | after |
|---|---|---|
| `Sort` below `WindowAgg`, TPC-DS | **0** | **13** (PG: 6) |
| TPC-DS `GroupAggregate` | 13 | **13** (unchanged, as predicted) |
| TPC-DS parity | 0 / 72 / 0 / 24 / 3 | unchanged |
| TPC-H parity | 2 / 20 / 0 / 0 | unchanged |
| TPC-H values | baseline | 22/22 byte-identical |
| TPC-DS SF0.5 values | baseline | `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` |

DESIGN §6 predicted: windowed queries gain the Sort (**yes**, 13);
`GroupAggregate` does **not** move because that needs slice (B)
(**correct** — 13 before, 13 after); match count unchanged (**correct**);
values unchanged (**correct**, and this was the round's real risk).

## 2. What landed

- `optimizer.WindowAgg` gains `Presorted`, defaulting to **false** so
  every construction that predates this round keeps sorting internally
  and stays correct.
- `createWindowPlan` stacks the `Sort` that
  `create_one_window_path` (`planner.c:4620`) stacks, on
  `PARTITION BY ++ ORDER BY`, and sets the flag.
- `windowOp` skips its private `sort.SliceStable` when the flag is set.

`costWindow` already priced this sort, so the round makes a node **exist**
for a cost that was already charged rather than introducing a new one.

### 2.1 PG's "pathkeys already satisfied" rule, added after a test caught me

The first implementation stacked a Sort before *every* window, which
broke `TestCreateWindowPathsEmitsTheSameChainOverTheSameInput`. The test
was right: PG only sorts when the required ordering is not already
present, so a window **chain** whose consecutive specs share an ordering
sorts once, not once per level. Added `childDeliversSortKeys`, which
recognises a `*Sort` on those exact keys and a `*WindowAgg` already
ordered on them, and treats every other node as unordered — failing
toward a redundant Sort, never toward a wrong one.

That the existing test caught this is worth stating plainly: it is the
same class of over-eager change I would otherwise have shipped and
discovered on the corpus.

## 3. The one number that moved, and what it proves

goopg now emits **13** `Sort`s below a `WindowAgg`; PG emits **6**.

The gap is not a defect — it is slice (B) made visible. PG needs fewer
explicit sorts because in the other cases the node *below* already
delivers the order: a `GroupAggregate` that had to group anyway emits
its groups in key order, so PG's window gets its ordering free. goopg
must sort in all 13 because nothing below it is ever credited for an
ordering.

So R6 converted an invisible executor behaviour into a visible planner
one, and the residual 13-vs-6 is now a *measurement* of exactly how much
slice (B) is worth. Before this round that quantity could not be
observed at all.

## 4. Two tests whose invariants the round moved

Both were updated to preserve what they actually protect, not deleted:

- `TestCreateWindowPathsEmitsTheSameChainOverTheSameInput` is a pointer
  walk asserting the producer carries the pre-producer input through by
  **identity** rather than rebuilding it. That invariant is untouched;
  only its depth moved, so the walk now descends through the Sort. Two
  assertions were **added** while I was there: that the stacked Sort
  carries the window's own keys, and that both windows in the chain are
  marked `Presorted` (i.e. exactly one sort for the chain).
- A new executor pin, `TestWindowOpPresortedFlagControlsTheInternalSort`,
  covers both directions on one deliberately out-of-order fixture:
  without the flag `windowOp` must still sort (10,20,30), with it the
  arrival order must survive untouched (30,10,20). The fixture is
  out-of-order precisely so "sorted" and "passed through" cannot be
  confused.

## 5. Gates

- `go test ./internal/optimizer/` and `./internal/executor/` — green.
- TPC-H values 22/22 byte-identical (`values-tpch-r6.txt`).
- TPC-DS SF0.5 sweep all-zero (`values-tpcds-sf05-r6.txt`). This was the
  round's stated risk — moving a sort from executor to planner is
  exactly the change that can silently reorder output, and the TPC-DS
  checksum is computed over **unsorted** rows, so 57 ck-verified queries
  passing is a real result rather than a formality.
- Parity measured against the live references (K9/K10); all captures
  `VERIFIED` against the built binary (K5).

## 6. Filed — slice (B), now quantified

The remaining work is to let a node below satisfy the window's ordering
instead of the Sort, which is what converts `HashAggregate` into
`GroupAggregate` and closes the 13-vs-6 gap above. It needs the upper
planner to compare paths **by pathkeys**; today `createWindowPaths`
receives a finished `input Node` and `windowsetoppaths.go:19` records
that above the search seam the inputs are Nodes with no pathkeys. That
is an architectural seam, and it is now the single largest identified
lever left on TPC-DS plan parity.
