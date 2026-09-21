# The `outer-over-derived` relaxation is a NO-GO on the current tree (M0145-0018)

Status: **NO-GO, 2026-09-21.** The firewall was NOT relaxed. The task's own
precondition — a fresh E1 re-verification rather than a re-use of the earlier
measurement — caught a C-04a-class regression at SF1 that did not exist when
the owner's GO was given. Per the task text, that is an automatic no-go: keep
the firewall and escalate the measurement.

Task: `.ralph/fix_plan.md` M0145-0018. Kind: impl. Parent: M0145-0011.

## Precondition 1 — satisfied

**M0145-0013 landed** (loop 43, `c622b7398`): the seam admits pulled `*CTEScan`
leaves, so the derived-input population the relaxation is supposed to serve can
actually reach the firewall check.

## Precondition 2 — the fresh re-verification, and it FAILS

### The current fire set

Seam census, TPC-DS SF0.25, knob arm, all 99 queries: `outer-over-derived`
fires **3 times per run — Q77 (2) and Q78 (1)**, matching the task's "it was
12, then 3". `semianti-not-tail` is now 0 (M0145-0016), and the rest of the
table is `leaf-count` 20, `residual-hits-pad` 6.

### SF0.25 — clean, exactly as before

Knob arm, firewall ON vs OFF, same build, all 99 plans captured both ways:

```
exactly Q77 and Q78 move, no others
Q77   788 ms   44 rows  ck a833a50c97b66268   (both arms)
Q78  3869 ms   15 rows  ck 4a8a89aae2584676   (both arms)
                 851 ms and 3931 ms with the firewall off
Q78 join kinds IDENTICAL: 3 Hash Anti, 2 Hash, 2 Hash Left — no NL election
Q77 gains one Nested Loop Left Join at rows=1, cost 0.03 -> 0.06
```

This reproduces loop 39's SF0.25 evidence exactly.

### SF1 — the relaxation is catastrophic

Private clone of the SF=1 datadir, knob arm, same build, fresh server per arm:

```
              firewall ON            firewall OFF
Q77            5680 ms  44 rows       5760 ms  44 rows   ck 9bd1900a34ce55c5 both
Q78           29002 ms 100 rows      >1800 s   TIMED OUT
```

Q78 did not finish inside a 1800-second client timeout, against 29 seconds with
the firewall on. The plan says why — this is the C-04a shape verbatim:

```
Nested Loop Left Join  (cost=5494.86..1147565.07 rows=5731 width=240)
  Join Filter: (((cs_sold_year = ss_sold_year) AND (cs_item_sk = ss_item_sk))
                AND (cs_customer_sk = ss_customer_sk))
  ->  Hash Left Join  (cost=5494.86..26516.81 rows=10317 width=160)
  ->  (CTE Scan on cs)
```

All three equi-conditions are demoted from join conditions to a `Join Filter`
on a nested loop whose outer is 10,317 rows and whose inner is a CTE scan. With
the firewall on, that join is a `Hash Left Join` and the query is 29 s.

## Why this differs from M0145-0011's SF1 evidence

Loop 40 measured the same A/B at SF1 and found Q78 **stayed a hash join**
(50885 -> 48066 ms). That evidence was true then and is not true now. Three
things landed in between, all of which change what the search can reach:

- **M0145-0013** — the seam admits pulled `*CTEScan` leaves;
- **M0145-0014** — the nested-sublink pull-up recursion;
- **M0145-0016** — the stable leaf partition, which stopped declining the
  demoted-ANTI mid-chain shape Q78 contains.

Each one lets more of Q78's problem into the DP, and the DP now has a
join-order choice it did not previously have. Removing the firewall hands that
choice to a cost model that prices the nested loop at 1.1M and still prefers
it over the hash join.

**This is precisely what the precondition was written to catch.** The task
says "the population drifts — it was 12, then 3" and requires re-measurement
"at execution time, not assumed". Re-using loop 40's numbers would have landed
a 60x-plus regression on Q78 at SF1 with a green SF0.25 gate.

## What was NOT done, deliberately

`problemPairsOuterWithDerived` is untouched. `GOOPG_DERIVED_FIREWALL` stays,
default ON. No gate was re-baselined. The owner's GO was given on evidence that
the current tree has invalidated, so the decision needs re-taking, not
executing.

## For the owner

The relaxation is not blocked by a missing mechanism; it is blocked by the COST
MODEL. The estimates are now honest (M0145-0011 measured that: the firewall's
decline is what manufactured the rows=6/rows=4 epsilons), and the search still
elects a nested loop at an estimated 1.1M cost. Options, none of which the loop
should pick:

1. **Keep the firewall permanently** and document it as a cost-model backstop
   rather than a statistics one — which is what it has been measured to be.
2. **Narrow it to the shape**: veto a nested-loop path whose inner is a derived
   input, instead of declining the whole search problem. M0145-0011's scope (d)
   already listed this; the SF1 evidence now argues for it specifically,
   because the problem is the PATH the DP picks, not its admission.
3. **Fix the nested-loop pricing for a derived inner** and re-run this
   verification. The 1.1M estimate losing to nothing suggests the comparison,
   not the estimate, is where the fault sits.
