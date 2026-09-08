# R6 — the window's Sort belongs in the plan

*Round 6 of `../TODO.md`. Implements K12, the largest remaining planner
lever. (Numbering note: the TODO's R6/R7 storage items are deferred
behind this — they are on-disk work, and this is the planner round the
goal is chiefly about.)*

## 1. The defect, from goopg's own source

`windowOp` (`operators_window.go:14`) — *"drains child rows, sorts by
PARTITION BY/ORDER BY"* — materialises its entire input and sorts it
with `sort.SliceStable` (`:103`).

PG does not. `nodeWindowAgg` **assumes sorted input**, and
`create_one_window_path` (`planner.c:4620`) stacks a `Sort` (or
`IncrementalSort`) above the input to provide it. goopg's own
`windowsetoppaths.go:19` states the divergence plainly, as a known
deviation.

Three consequences, in increasing order of importance to this goal:

1. **The plan text differs from PG's** wherever a window appears — PG
   shows `WindowAgg -> Sort -> ...`, goopg shows `WindowAgg -> ...`.
2. **The whole input is materialised**, which PG's streaming node never
   does.
3. **The ordering requirement never reaches the planner** — and this is
   K12. Because no node below is ever asked for an ordering, no path is
   ever *credited* for delivering one, so goopg's sorted aggregate can
   never win the contest PG's wins constantly.

## 2. Why (3) is the whole aggregation inversion

Measured (R3 §2), TPC-DS Q12:

```
PG:     WindowAgg -> Sort (i_class) -> GroupAggregate
goopg:  WindowAgg               ->      HashAggregate
```

PG owes a `Sort` for the window's `PARTITION BY i_class` no matter what.
Having to pay it anyway, sorting **below** the aggregate makes
`GroupAggregate` nearly free — so it beats `HashAggregate` on a contest
`HashAggregate` cannot even enter, because it alone emits the required
order.

goopg's aggregate faces no such requirement, so `HashAggregate` wins on
raw cost, every time. That is why R3's spill arm — a correct, faithful
change — moved `GroupAggregate` only 1 -> 13 against PG's 100: it was
competing on the wrong axis.

## 3. Scope: this round does slice 1 only, and says so

The complete fix has two halves:

- **(A) Make the Sort explicit in the plan.** Planner emits it, executor
  stops doing it privately. Well-bounded. **This round.**
- **(B) Let a node below satisfy the requirement instead of the Sort.**
  This is what actually converts `HashAggregate` into
  `GroupAggregate`, and it needs the upper planner to compare paths *by
  pathkeys*. Today it cannot: `createWindowPaths` receives a finished
  `input Node`, and `windowsetoppaths.go:19` records that "above the
  search seam the inputs are finished Nodes with no pathkeys". That is
  an architectural seam, not a cost bug. **Not this round**, and the
  round must not pretend otherwise.

So the honest prediction is in §6: (A) alone moves plan *text* toward PG
and removes a materialisation, but will **not** by itself flip
aggregates. Stating that in advance matters, because a round that
delivers a real improvement and no match-count movement will otherwise
read as a failure.

## 4. The change

**Planner** (`windowsetoppaths.go`, `createWindowPaths`/`addWindowPaths`):
for each window in the chain, stack a `Sort` on the window's
`PARTITION BY` ∪ `ORDER BY` keys when the input does not already
provide them, exactly as `create_one_window_path` does. The sort is
already **priced** here — `windowsetoppaths.go` says the window sort is
"PRICED for the first time" — so this round makes the priced thing
*exist* rather than inventing a new cost.

**Executor** (`operators_window.go`): `windowOp` stops sorting when the
plan says the input is already ordered. A `Presorted bool` on
`optimizer.WindowAgg`, set by the producer that stacked the Sort,
mirroring the presorted-group predicate the sorted `Aggregate` path
already uses (E-15). Fail-closed: when the flag is absent or false,
`windowOp` sorts as it does today, so any path that reaches the executor
without the new producer is still correct.

Not changed: frame evaluation, the window functions themselves, peer/
group-boundary logic, `SetOp`, and every cost function.

## 5. Gates

- Unit: (a) a windowed plan now contains a `Sort` below the `WindowAgg`
  with the partition/order keys; (b) `Presorted` is set exactly when the
  producer stacked that Sort; (c) fail-closed — a hand-built `WindowAgg`
  with no flag still sorts internally, pinned by an out-of-order input;
  (d) no double sort (the flag is set iff the Sort exists).
- Suites: optimizer + executor.
- **Values, both corpora, all-zero.** This is the round's real risk:
  window results are order-sensitive, and moving the sort from executor
  to planner is exactly the kind of change that silently reorders
  output. TPC-DS has 13 windowed queries and its checksum is
  order-sensitive by design (`ck` is computed over unsorted rows), so
  the sweep is a genuine test here, not a formality.
- Parity, both corpora, against the live references (K9/K10).

## 6. Prediction, in advance

- TPC-DS windowed queries (Q12, Q20, Q44, Q47, Q51, Q53, Q57, Q63, Q70,
  Q86, Q89, Q98) gain a `Sort` below the `WindowAgg`, moving their text
  toward PG's.
- **`GroupAggregate` adoption does NOT move materially**, because that
  needs slice (B). If it *does* move, my §3 reading of the seam is
  wrong and the report must say so.
- Match count: likely unchanged. These queries differ in several places
  at once; fixing one is not expected to close any of them outright.
- Values: unchanged. Any movement is a bug in this round, not a finding.

## 7. Review record

Subagent delegation remains unavailable in this environment (`Task` not
exposed; recorded since R0). Adversarial self-review, with the checks
actually run:

- **The executor's internal sort was read, not assumed**
  (`operators_window.go:14` doc comment and the `sort.SliceStable` at
  `:103`) — this workstream has now falsified three of my claims that
  came from reading a comment instead of the code, so the code was read
  in both places.
- **The seam claim in §3(B) is quoted from the file that owns it**
  (`windowsetoppaths.go:19`), and it is the reason the round is split
  rather than an excuse for stopping early.
- **The "already priced" claim was checked** against
  `windowsetoppaths.go`'s own statement that the window sort is priced
  today, so slice (A) adds a node to match a cost that already exists
  rather than adding a cost.
- **Falsification named** (§6): if aggregates flip on slice (A) alone,
  the design's model of the seam is wrong.
