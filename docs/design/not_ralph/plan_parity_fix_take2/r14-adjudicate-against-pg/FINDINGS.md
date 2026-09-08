# R14 — adjudicating the flip against PG, on evidence

*Round 14 of `../TODO.md`. Findings round. Adjudicates the first of
R12's four "real plan movement" failures the way the goal requires —
against PG, not against the new output.*

## 1. The criterion, restated

R12 left four tests failing under the flip with genuine plan
differences, and specified the adjudication: *"the question is whether
the shape the flip produces is the one PG produces, which is the goal's
actual criterion."* Values passing cannot answer that — it distinguishes
a correct plan from a broken one, not a PG-shaped plan from a merely
correct one.

`TestSlice3LiveQ9ShapeDerivation` is built on TPC-H Q9, so PG's own
answer is already captured and the question is directly checkable.

## 2. Q9, three ways

Join structure only, costs stripped:

**PG 18.3**
```
Parallel Hash Join
  -> Parallel Seq Scan on orders
     Join Filter: (supplier.s_suppkey = lineitem.l_suppkey)
     -> Hash Join
        -> Hash Join
           -> Parallel Hash Join
              -> Parallel Seq Scan on partsupp
                 -> Parallel Seq Scan on part
              -> Seq Scan on supplier
           -> Seq Scan on nation
```

**goopg, flip ON**
```
Parallel Hash Join
  -> Parallel Hash Join
     -> Parallel Hash Join
        -> Parallel Seq Scan on orders
        -> Hash Join
           -> Seq Scan on lineitem
           -> Seq Scan on part
     -> Seq Scan on partsupp
  -> Hash Join
     -> Seq Scan on supplier
     -> Seq Scan on nation
```

**goopg, flip OFF (today's shipping default)**
```
Hash Join
  -> Hash Join
     -> Parallel Seq Scan on orders
     -> Hash Join
        -> Seq Scan on partsupp
        -> Hash Join
           -> Seq Scan on lineitem
           -> Seq Scan on part
  -> Hash Join
     -> Seq Scan on supplier
     -> Seq Scan on nation
```

## 3. Verdict on the mechanism: the flip is the PG-faithful direction

PG's Q9 uses **`Parallel Hash Join`** — twice. goopg with the flip uses
it three times; goopg without it uses **none at all**, and its only
parallelism is a single `Parallel Seq Scan on orders` stamped by the
post-pass.

So on the one query where the comparison is directly available, the flip
moves goopg's parallel-join mechanism from *absent* to *present*, which
is the direction PG defines. This is the first **evidence** for R10's
argument, which until now rested on the design principle alone (PG has
no Gather post-pass, so running one while trying to reproduce PG's
output is reaching for the same answer by a different method).

## 4. Verdict on the join ORDER: unchanged by the flip, and still wrong

Both goopg shapes join `lineitem` with `part` first and bring `partsupp`
in later; PG starts from `partsupp ⋈ part`, adds `supplier`, then
`nation`, and joins `orders` last with a `Join Filter` on
`s_suppkey = l_suppkey`.

The flip does not fix that and was never going to — join order is
`join-order`, TPC-H's other joint-top divergence category at 18, and it
is a separate problem from parallelism. **Q9 does not become a MATCH
under the flip**, which is exactly what R10 `DESIGN.md` §6 predicted in
advance ("match count: I expect 2 and 0, i.e. no movement").

## 5. What this means for the four tests

`TestSlice3LiveQ9ShapeDerivation`'s failure — *"unexpected narrow build
[o_orderkey o_orderdate]"*, *"missing narrow build [l_discount n_name
…]"* — is a **consequence of a different, more PG-like join shape**, not
a defect. Narrow-build selection follows the build sides, and the build
sides moved. The test encodes the pre-flip shape.

That is a re-baseline, but a *justified* one: the new shape is closer to
PG on the axis the flip addresses, and unchanged on the axis it does
not. The remaining three (`TestSlice3FilterColumnSurvivesNarrowing`,
`TestSplitEqualityForHashMultiKey`,
`TestOwnedBuildPoisonPrebuiltBoundary`) need the same treatment, and
`TestSplitEqualityForHashMultiKey`'s *"fell back to Nested Loop"* is the
one to be most careful with — losing a hash join is a shape regression
unless PG also declines it there.

## 6. Filed

- **R15 — adjudicate the remaining three** the same way, then
  re-baseline the justified ones and land the flip together with the
  provenance regeneration (K20) and the stage pin, in one commit.
- Note for that round: `TestSplitEqualityForHashMultiKey` is a
  synthetic multi-key fixture, not a corpus query, so PG's answer must
  be obtained by running the equivalent SQL on :65432 rather than read
  off an existing capture.
