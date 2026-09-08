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

**Start by re-running `TestPreDPPinnedSemiKeysResolveAfterDP`** and
understanding what it asserts, before touching anything. It is the
named, measured obstacle, and slice 2b is the cautionary tale for
meeting one blind.

**Verify first, per this workstream's repeated lesson**: instrument that
the synthesised clause reaches the DP and that `{part}|{partsupp}` is
enumerated, *before* judging any plan change. Four attempts at slice 2b
were each wrong until the input was measured rather than assumed.

**Expect no match-count movement** (`ROADMAP-to-all-match.md`): Q9 also
differs in join-method, scan-type, parallelism and rendering. The
round's success test is `join-order` falling on both corpora, not a
match.
