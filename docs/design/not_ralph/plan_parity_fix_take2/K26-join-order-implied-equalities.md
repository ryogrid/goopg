# K26 — join-order's dominant cause: no implied join equalities

*Investigated 2026-09-09, measured on TPC-H Q9. This is the input for
the join-order round, which the roadmap ranks as the largest blocker
(95/99 TPC-DS, 17/22 TPC-H).*

## 1. Measured

`GOOPG_PGSHAPED_DP_TRACE=1` on TPC-H Q9. Every pair the DP enumerates
contains `lineitem`:

```
{lineitem}|{part}   {lineitem}|{supplier}   {nation}|{supplier}
{lineitem}|{partsupp}   {lineitem}|{orders}   {lineitem+part}|{supplier} ...
```

and there are **20 declines, every one `reason=no-join-clause`**.

`{part}|{partsupp}` is **never enumerated**.

## 2. Why, and why PG does not have the problem

Q9's join clauses all run through `lineitem`:

```
p_partkey = l_partkey
ps_partkey = l_partkey AND ps_suppkey = l_suppkey
s_suppkey = l_suppkey
o_orderkey = l_orderkey
s_nationkey = n_nationkey
```

So `part` and `partsupp` have **no direct join clause** — they are
connected only via `lineitem`. goopg therefore declines the pair, and
`partsupp ⋈ part` is not a join order it can reach at any cost.

PG reaches it. Both `p_partkey` and `ps_partkey` are equated to
`l_partkey`, so they are in one **equivalence class**, and
`generate_join_implied_equalities` (`equivclass.c`) SYNTHESISES
`p_partkey = ps_partkey` as a join clause. PG's own Q9 plan uses it —
its innermost join is `partsupp` with `part`.

## 3. goopg has equivalence classes, but not this consequence

`equiv_class.go` exists (M0075-0001, "equivalence-class inference for
transitive …") and is used from `joinrestrict.go:180`. The seam's
comment says what it supplies: *"take2 P1-20: give the SEARCH the
equivalence class's CONSTANTS."*

**Constants, not derived join clauses.** goopg infers `a = 5` from
`a = b AND b = 5`. It does not infer `a = c` from `a = b AND b = c` and
hand it to the join search as a join clause.

That single gap is what makes the DP's reachable join orders a strict
subset of PG's, on every query whose joins are star-shaped around one
fact table — which is most of TPC-H and essentially all of TPC-DS.

## 3a. CORRECTION — it was deliberate, and it has a known first obstacle

§3 implied the transitive half was simply absent. **It was considered,
measured, and deferred**, and the seam says so six lines below the
comment quoted above:

> *"CONSTANTS ONLY, deliberately. Propagating `a = 42` across a class
> only adds restrictions and re-opens no join order. Adding the
> transitive `a = c` would hand the search new JOIN clauses and reshape
> plans broadly — **measured: it broke the pinned-semi-join layout
> `TestPreDPPinnedSemiKeysResolveAfterDP` asserts**, on a query
> containing no constants at all. That half stays on its legacy caller
> pending its own evaluation."*

Three things follow, all of which make the round easier rather than
harder:

1. **The mechanism already exists** on a legacy caller — this is an
   evaluation and a re-wiring, not a port from scratch.
2. **The round's first obstacle is named in advance**:
   `TestPreDPPinnedSemiKeysResolveAfterDP`. Exactly as slice 2b's
   assertion was, and that one took four attempts precisely because it
   was met blind.
3. **"Pending its own evaluation" is the round this workstream is for.**
   The deferral's stated reason is that it "reshapes plans broadly" —
   under the previous policy that was a risk; under this goal's rule a
   plan that matches PG is not a regression however it reshapes.

Reading the deferral before writing the design is what turns a
"missing feature" into a scoped task with its blocker known. Both of my
previous roads into unfamiliar code (K11a's stale comment, slice 2b's
assertion) went wrong by not doing this first.

## 4. Why this is the right next round

- It is the **dominant** category (95/99, 17/22) and untouched.
- The cause is now specific: a missing PG mechanism with a named oracle
  function, not a costing difference. Recall root-causes' central
  finding was that goopg's problems were mostly costing, not missing
  candidates — **join-order is the exception**, and this is the
  evidence.
- It is *candidate generation*, so it cannot be reached by any amount
  of cost work. The 20 `no-join-clause` declines on Q9 are pairs the
  cost model never sees.

## 5. What the round must do

Re-wire the EXISTING transitive-equality half (§3a) from its legacy
caller into the seam, so the DP receives the derived join clauses —
PG's `generate_join_implied_equalities` (`equivclass.c`) shape: for
each equivalence class, the join clauses between members on different
relations.

**Read `TestPreDPPinnedSemiKeysResolveAfterDP` first — done, §6.**

**Verify first, per this workstream's repeated lesson**: instrument that
the synthesised clause reaches the DP and that `{part}|{partsupp}` is
enumerated, *before* judging any plan change. Four attempts at slice 2b
were each wrong until the input was measured rather than assumed.

**Expect no match-count movement** (`ROADMAP-to-all-match.md`): Q9 also
differs in join-method, scan-type, parallelism and rendering. The
round's success test is `join-order` falling on both corpora, not a
match.


## 6. The named obstacle, read (2026-09-09)

Doing what §5 prescribes, before writing any code. The test's own
comment:

> *"the mandatory F8 remap test: DP reorders the outer layout below the
> pinned semi join; every `ColumnRef` in the semi join's keys/predicate
> must resolve to the column of the same name in the post-DP outer
> schema."*

and its fixture:

```sql
SELECT b1_k FROM big1, big2, small3, small4
WHERE b1_j = b2_j AND b2_j = s3_j AND s3_k = s4_k
  AND EXISTS (SELECT 1 FROM inner_e WHERE e_k = big1.b1_k)
```

`b1_j = b2_j AND b2_j = s3_j` is an equivalence class over
{b1_j, b2_j, s3_j}, whose transitive closure hands the DP `b1_j = s3_j`
— a join clause that lets it reorder the outer layout in ways it
otherwise could not.

**So the test is a REMAP test, not a layout-correctness test.** It does
not assert that some particular join order is right. It asserts that
after the DP reorders, the pinned semi join's column references still
resolve by name in the new outer schema.

That reframes the obstacle, and favourably:

- The derived equality is **not wrong**. The reorderings it enables are
  legitimate — they are the ones PG has and goopg lacks (§2).
- What breaks is the **pinned semi join's remap**, which is incomplete
  for the wider set of layouts the new clauses make reachable.
- So the round is: derive the equalities, then extend the F8 remap to
  survive the reorderings they enable. That is a bounded, named piece
  of work on a mechanism that already exists — not a re-litigation of
  whether transitive equalities belong in the search.

The seam's deferral note said the transitive half "broke the
pinned-semi-join layout the test asserts". Read closely, the test
asserts a **remap invariant**, and a remap that cannot follow a legal
reordering is a gap in the remap. Recording the distinction because it
decides where the next round spends its effort: in `predp`'s remap, not
in `equiv_class.go`.


## 7. Measured with the transitive half ENABLED (2026-09-09)

Switched the seam from `inferEquivClassConstants` to
`inferTransitiveEqualities` and ran the suites. The deferral note said
this "reshapes plans broadly". Measured today, it breaks **three
tests**, not a corpus:

1. `TestPreDPPinnedSemiKeysResolveAfterDP`
2. `TestSlice3LiveQ9ShapeDerivation`
3. `TestSlice3SelfJoinInDerivedTable`

That is a materially smaller blast radius than the note implies, and
worth re-measuring precisely because the note is old and the tree has
moved a long way since.

### 7.1 The named obstacle fails INSIDE THE TEST

`TestPreDPPinnedSemiKeysResolveAfterDP` does not fail an assertion. It
**nil-dereferences in its own helper**: `findSpineSemi` walks
`Filter`/`Project`/`Sort` and returns `(nil, nil)` at the first `*Join`
that is not Semi/Anti, and the caller dereferences the result without
checking.

So the failure presents as a segfault that looks like a planner crash
and is not one — the fourth time this session a test-side walker has
produced a planner-shaped symptom (K19).

Extending the walker to descend a non-semi join's children (committed —
it is strictly more correct regardless) is **not sufficient**: the semi
join is still not found. So it is not merely relocated above/below an
inner join; with implied equalities the EXISTS is unnested to a
different shape entirely, or the pinned semi join is gone.

**That is the open question, and it is now a specific one**: where does
the pinned semi join go when the DP is given implied join equalities?
Answer it before touching the remap — the F8 remap may not even be the
thing that needs changing, and §6's conclusion that it is should be
treated as a hypothesis, not a finding.

### 7.2 The other two

`TestSlice3LiveQ9ShapeDerivation` and `TestSlice3SelfJoinInDerivedTable`
are narrow-build/keep assertions of the same family R14 adjudicated as
justified re-baselines when the join shape legitimately moves. They
should be adjudicated against PG the same way, not assumed.

### 7.3 State

The seam is REVERTED to constants-only; suites green. The walker fix is
kept. Nothing about the default behaviour changed.
