# R17 — correction: goopg does NOT lose the hash join on real data

*Round 17 of `../TODO.md`. Findings round. Corrects R16/K21, which
over-generalised from a synthetic fixture.*

## 1. The correction

R16 concluded, and K21 recorded, that the flip *"costs a hash join PG
keeps"*. **That is wrong for the corpus.** Running the same statement
against the real SF=1 data with the flip on:

```
Gather  (Workers Planned: 3)
  -> Parallel Hash Join
       Hash Cond: ((ps_partkey = l_partkey) AND (ps_suppkey = l_suppkey))
       Join Filter: (ps_availqty > s)
       -> Parallel Seq Scan on partsupp ps
       -> GroupAggregate
            Group Key: l_partkey, l_suppkey
            -> Index Scan using lineitem_part_supp_fkidx on lineitem
```

goopg hash-joins on **both** equalities, with the residual as a
`Join Filter` — exactly PG's structure. There is no nested-loop
fallback on real data. The fallback exists **only** at the test
fixture's synthetic cardinalities.

## 2. Side by side with PG

| | goopg (flip on) | PG 18.3 |
|---|---|---|
| `Gather` / `Workers Planned` | yes / **3** | yes / **3** |
| join | **Parallel** Hash Join | Hash Join |
| `Hash Cond` | `(ps_partkey = l_partkey) AND (ps_suppkey = l_suppkey)` | **same** |
| `Join Filter` | `ps_availqty > s` | **same** |
| outer | `Parallel Seq Scan on partsupp` | **same** |
| inner aggregate | `GroupAggregate` | `HashAggregate` |
| inner scan | `Index Scan using lineitem_part_supp_fkidx` | `Seq Scan` |

Four of seven rows are identical including the worker count, and the
join condition and residual placement match exactly. The remaining
differences are the join's parallel-awareness, and an
aggregate-strategy plus scan-type choice on the inner side.

This is the closest goopg has come to a PG plan on any non-trivial
shape in this workstream.

## 3. How I got R16 wrong

R16 §3 stated the caveat correctly:

> *"the fixture supplies synthetic, smaller row counts and this
> measurement used the real cluster's. The SHAPE question is settled;
> the THRESHOLD question is not."*

Then its headline and K21 asserted the flip "costs a hash join PG
keeps" — a claim about the threshold case, generalised to the corpus,
in the same document that had just said the two were different
questions. Writing the caveat did not stop me from reasoning past it.

That is the ninth falsified claim in this workstream, and the failure is
a new variant: not a missing check, but **a stated limitation ignored
one paragraph later**. K4 covers "conclude from reading without
checking"; this needs its own line, because the check was done and the
conclusion still outran it.

## 4. What is actually true about the flip

- **Gain (R14)**: Q9 gains the `Parallel Hash Join` PG uses; without
  the flip goopg emits none at all.
- **Gain (here)**: the multi-key shape is near-identical to PG's,
  including worker count and residual placement.
- **Open**: `TestSplitEqualityForHashMultiKey` still fails, but as a
  **fixture-cardinality** question — whether the fixture's counts still
  keep the comparison off the one-row floor once partial paths are
  priced. That is a test-fixture matter, not a corpus regression.
- **Unmeasured**: R8's `aggregation-strategy` 10 → 14 on TPC-H, still
  the one genuine open concern about the flip.

## 5. Filed

- **K21 is superseded** by this round; the TODO entry is corrected in
  place rather than deleted.
- **R18 — the fixture question**: does the synthetic fixture still test
  what it means to under the flip? If its counts now sit on the wrong
  side of the one-row floor, the fix is the fixture, not the planner.
- **R19 — explain `aggregation-strategy` 10 → 14**, the last unexplained
  objection to landing the flip per R10 `DESIGN.md` §7.
