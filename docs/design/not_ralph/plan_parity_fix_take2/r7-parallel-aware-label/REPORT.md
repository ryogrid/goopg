# R7 results — the premise was wrong, and that is the finding

*Round 7 of `../TODO.md`. Design: `DESIGN.md` (committed `a50e9cdbe`).
Implemented, gated and measured 2026-09-08.*

## 1. Verdict

The plumbing landed and is correct. **The premise it was built on was
falsified by the measurement it was designed to make.**

| | goopg | PG |
|---|---|---|
| `Parallel Hash Join`, TPC-H | **0** | **9** |
| `Parallel Hash Join`, TPC-DS | **0** | **139** |

After threading `Path.ParallelAware` onto the plan node and rendering
PG's prefix, goopg emits the label **zero times on either corpus**. Q14
is still `SHAPE-DIFF [parallelism]`; parity is unchanged everywhere
(TPC-H 2/20/0/0, TPC-DS 0/72/0/24/3). Values green on both (TPC-H 22/22
byte-identical; TPC-DS `PASS=95` with every failure counter zero).

DESIGN §6 said: *"if Q14 still prints `Hash Join`, its path is not
parallel-aware and that is the finding."* That is what happened, and it
is a more useful result than the label would have been.

## 2. What the zero means

`addPartialHashJoinPath` (`joinpathsparallel.go:195`) does set
`ParallelAware: true`, and the hash arm of `createHashJoinPlan`
(`createplanjoin.go:551`) is the site now carrying it onto the node —
checked, because three Join construction sites exist and patching one of
three would have produced exactly this zero for a trivial reason. It is
the right site: it is the one holding `assertParallelAwareJoinIsRunnable`.

So the flag is genuinely **false** for every hash join goopg plans, and
those joins do not come from the partial-path producer at all.

The mechanism is visible in the plans. goopg's TPC-H output carries 12
`Parallel Seq Scan`s and 4 `Hash Join`s under `Gather` nodes — and the
`Parallel Seq Scan` label is not a partial path's doing either: it is
stamped by `stampParallelScan` at **Gather-construction time**
(`createplangather.go:112`), a copy-on-write walk over an
already-built tree.

**goopg parallelises by stamping a Gather over a serial subtree; PG
parallelises by building partial paths and letting them win.** That is
why `parallelism` is TPC-H's joint-top divergence category at 18: it is
not a labelling gap, it is a different mechanism for producing parallel
plans, and the label was the visible edge of it.

## 3. Was the round worth landing?

The code is worth keeping on its own terms, independent of the premise:

- `optimizer.Join` previously **discarded** `Path.ParallelAware` at plan
  construction. That is a real loss of planner information, and PG's
  `Plan` carries the flag precisely because EXPLAIN and the executor
  both need it.
- The renderer now implements PG's actual rule
  (`explain.c:1630` — a generic per-node prefix), which goopg already
  applied to `SeqScan`. The two arms now agree instead of one silently
  omitting it — the sibling-divergence class that has bitten this
  project repeatedly.
- The moment a partial hash-join path *does* win, the label appears with
  no further work. The plumbing is the prerequisite for R8, not a
  dead end.

What the round did **not** buy is any parity movement, and the report
says so plainly rather than presenting the plumbing as progress toward
the goal.

## 4. Correction to K15

K15 read: *"the top TPC-H category may be a LABEL"*, with the premise
explicitly flagged unverified. It is not a label. Corrected in the TODO:
the category is a **path-mechanism** difference (Gather-stamping vs
partial paths), which is a substantially larger and more interesting
item, and it now has direct evidence rather than an inference from a
missing string.

This is the fifth of my hypotheses falsified in this workstream. It is
also the second in a row that was labelled a hypothesis in advance and
killed by the check it asked for — R5 and R7 — rather than surviving
into a report as an assertion. That is the pattern working.

## 5. Gates

- `go test ./internal/optimizer/` and `./internal/executor/` — green,
  plus a new pin (`TestJoinParallelAwareLabel`) whose load-bearing half
  is the OFF cases: a join that is not parallel-aware must never claim
  to be, since the label states a fact about cooperative hash building.
- TPC-H values 22/22 byte-identical (`values-tpch-r7.txt`).
- TPC-DS SF0.5 sweep all-zero (`values-tpcds-sf05-r7.txt`) — run even
  though the change is a rendered string plus a field no executor reads,
  because "no executor reads it" was itself an assumption worth testing
  cheaply.
- Parity against the live references; all captures `VERIFIED`.

## 6. Filed — R8 supersedes R7's original target

`parallelism` (18 on TPC-H) is now attributable: goopg has no partial
hash-join path winning anywhere on either corpus. The next question is
whether `addPartialHashJoinPath` is never called, called and declined,
or called and outcompeted — three different fixes, and the memory
`planner_verify_both_candidates_generated` says to instrument `addPath`
rather than theorise about the cost functions. That is R8's first step.
