# E-15 (EX3-07) — Presorted-prefix input contract

Status: accepted (contract published). Implements TODO_ALL.md E-15.

## 1. Why this exists

Take3 13 §8.7 tiebreaker: the planner's Incremental Sort (C-14) is BLOCKED
on executor support, and executor support was waiting on the planner —
the two sides waiting on each other does not work. This item breaks the
wait unconditionally: the executor publishes what a future Incremental
Sort input must guarantee (and what the executor will guarantee back) as
a doc + contract test against CURRENT sort behaviour. C-14 consumes it;
E-03 (conditional implementation) files only if C-14 activates.

## 2. Oracle

PG `create_incremental_sort_path` (`pathnode.c`): `presorted_keys` =
leading pathkeys already satisfied, via `pathkeys_count_contained_in`
(take3 01 §4.1). Executor (`nodesort.c` IncrementalSort): input must
arrive ordered by the presorted prefix; rows with equal prefix values
form a group; each group is sorted with a full tuplesort; groups stream
in prefix order. Direction/NULL placement are per-key properties of the
ORDER BY; equality (group framing) is direction-insensitive with NULLs
equal.

## 3. Current goopg behaviour (the baseline E-03 implements against)

- No `IncrementalSort` node, no presorted-keys field, no presorted
  awareness anywhere: `sortOp` (`internal/executor/operators.go:783`)
  always fully sorts (in-memory chunk + spill + N-way merge).
- Order semantics: per-key NULL placement (`NullsFirst`), else
  `compareDatum` with `Desc` flip; both-NULL = equal/continue; stable
  (`SliceStable`); key values evaluated once per row (`keyvals`).
- `enable_incremental_sort` GUC exists as an accepted-and-ignored stub
  (no producer reads it —
  `internal/optimizer/enable_scan_methods_test.go:19`).

## 4. The contract

Input guarantee (planner/C-14 half): input ordered by the first n keys
under each key's own direction + NULL placement (exactly what sortOp
emits for the prefix today); equal-prefix rows contiguous (a group);
groups in prefix order. Equality = `sortPrefixEqual`
(`internal/executor/sort_presorted.go`): per-key `compareDatum == 0`,
both-NULL equal, direction-insensitive.

Executor guarantee (E-03 must deliver): output fully ordered over ALL
keys, sequence-identical to full sort (order-equivalence — pinned by
`TestPresortedInputOrderEquivalence` against current behaviour); peak
memory bounded by the largest group (single-group input degrades to a
full sort, still correct); incomparable key pairs split groups, never
merge them and never error (over-split costs perf, merge corrupts
order).

Non-guarantees: cross-group stability beyond today's full sort; spill
framing interplay (decided at E-03, groups compose with — not inside —
chunk discipline); costing (C-14); parallel.

## 5. Code half

`sort_presorted.go`: contract text + `sortPrefixEqual` (pure,
uncalled by production — first caller is E-03). `sort_presorted_test.go`:
equality table (direction/nulls/clamp/incomparable-split) +
order-equivalence oracle (presorted-prefix input ≡ shuffled input,
fully ordered, group framing agrees with emission).

## 6. Gate (this item)

Contract doc + test, NO behaviour change: executor + optimizer suites
green; no plan-shape, values, or timing arms (nothing executes
differently). E-03's gate (when activated): group-wise order-equivalence
vs this oracle + memory-bounded proof + values-diff R8.
