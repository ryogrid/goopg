# Q9's premise is inverted: goopg's estimate is 1.4x off, PG's is 344x off

Settled before rev 3 was implemented, from data already committed in this
directory. Review made "get the actual row count" a binding condition on
the scope (F18), naming three possible outcomes. The answer is the third:
**the premise inverts.**

## The number

`../r128-parity-over-throughput/sf1-values-ON.txt` and `-OFF.txt`, Q9:

```
Q9: OK elapsed=4.71s ... rows=175
```

TPC-H Q9 at SF=1 returns **175 rows** (nation x year), identical on both
arms. Against the top-node estimates recorded in
`../r126-fk-persistence/step-d-recon-fk-chain-is-a-no-go.md:50-52` and
`../r128-parity-over-throughput/tpch-default-ON.plans.txt`:

| | estimate | vs actual 175 |
|---|---|---|
| goopg, no FKs | 122 | **1.4x low** |
| goopg, current default | 97 | 1.8x low |
| goopg, 8 FKs declared | 5,000 | 28.6x **high** |
| **PG 18.3** | **60,125** | **344x high** |

## What this kills

**"A more accurate Q9 cardinality produces a less PG-like plan" is
FALSE**, and with it R130 rev 3's title, §1 and §2. The FK arm did not
improve the estimate — it moved it from 1.4x low to **28.6x high**, i.e.
**20x further from truth**. A worse estimate produced a worse plan. There
is no paradox to explain.

The error was inherited, not invented: R126's recon wrote "41x closer to
PG" and rev 3 silently read that as "41x more accurate". **Closer to PG
is not more accurate** when PG is itself 344x out. This is the same
subterm-vs-total family that sank rev 1 (a subnode cost against a plan
root) — here, an estimate compared against another estimate rather than
against ground truth.

Note also that R125's "ground truth 318,748" is a *different node* (the
contested `partsupp ⋈ lineitem` join), not the top-node figure the recon
and rev 3 were comparing. Both numbers are real; conflating the levels is
how the framing went wrong.

## What this opens, and it is bigger than Q9

**On Q9, goopg's cardinality estimator is dramatically BETTER than PG's**
— 1.4x versus 344x. So Q9's `join-order` divergence is not goopg failing
to estimate well enough. It is at least partly goopg estimating *well*
where PG estimates *catastrophically*, and then, correctly, choosing a
different plan.

That reframes the goal for this query, and possibly others:

> **Matching PG's plan on Q9 may require reproducing a 344x PG estimation
> error.** The standing goal accepts slower plans, but it says nothing
> about whether goopg should adopt PG's *mistakes* to inherit its shapes.

This is a question for the goal's owner, not one to settle by scoping
another round. It is recorded here rather than resolved.

## Consequences for the programme

1. **Any round that moves an estimate "toward PG" must state the actual**
   alongside it. Three documents in this directory (R125 §, R126's recon,
   R130 rev 3) reasoned about goopg-vs-PG estimate gaps without one.
2. **The accidental-agreement hypothesis needs re-basing.** Rev 3 §2
   argued the 6/22 might be propped up by compensating errors. Review
   already showed Q9 cannot test that (it is not one of the six). The
   measurement above adds that Q9's agreements coexist with an estimate
   that is nearly *right*, which is the opposite of the hypothesised
   mechanism. The programme-level question stands, but its witness set is
   the six matching queries and its framing must be re-derived.
3. **`pg-plan-parity-diff.py` is shape-only by design** (N1:
   "estimates stripped from shape"). That is correct for measuring
   parity, and it is precisely why an accuracy regression can hide inside
   a parity improvement, and vice versa. Neither metric substitutes for
   the other.

## Status

R130 is **withdrawn at rev 3** and not re-scoped in this document. The
next round should be chosen against the reframing above, with the goal
owner's view on the "reproduce PG's error?" question if one is available.
