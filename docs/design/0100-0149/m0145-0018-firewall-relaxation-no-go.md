# The `outer-over-derived` relaxation is a NO-GO on the current tree (M0145-0018)

Status: **EXECUTED 2026-09-23.** The owner GO below was executed: the
`outer-over-derived` decline (`problemPairsOuterWithDerived`, including its
Semi/Anti arms, plus `leafIsDerivedInput`, which had no other reader) is
removed from `internal/optimizer/relfromjoinlist.go`, and
`GOOPG_DERIVED_FIREWALL` is retired through the standard provenance channel
(resolver deleted; order entry kept so older artefacts still decode; stamp
`retired(M0145-0018)`). See §"Execution (2026-09-23)" at the end for the
default-arm gate record.

Status history: OWNER GO 2026-09-22 — executable. NO-GO 2026-09-21;
RE-VERIFIED 2026-09-22 and the 2026-09-21 blocker is GONE. The single
remaining criterion-1 violation (Q77's degenerate `rows=1` Append-branch NL
election, +16% on a ~5 s query, values byte-identical, no timeout) is WAIVED
as measured-benign by the owner (progress-report review §3.1, option (a));
the criterion is re-scoped to "no NL election on a NON-degenerate join
condition and no timeout-class move". See §"Fresh E1 re-verification
(2026-09-22)" below. The firewall itself was not relaxed on either
measurement date.

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


---

# Fresh E1 re-verification (2026-09-22, loop #82)

The banner's OWNER GO says 0018's fresh E1 re-runs once 0019 lands. 0019 and
its fix 0019a landed, so this is that re-run — measured, not re-used.

## The fire set, re-derived (the task insists it drifts)

SF0.25 knob arm, `GOOPG_PGSHAPED_DP_TRACE=1`, all 99 queries:

```
SEAM-DECLINE-CENSUS: timeout=600 logs=1 classes=3 declines=17
    10 reason=leaf-count
     3 reason=outer-over-derived
     4 reason=residual-hits-pad
```

With `GOOPG_DERIVED_FIREWALL=off` the class disappears entirely
(`classes=2 declines=14`), so the census is measuring what it claims to.

Deriving the affected queries from the A/B rather than from trace attribution
(the server log carries no query markers) gives a **plan-diff trap worth
recording**: a naive diff reports **five** changed queries — Q36, Q70, Q77,
Q78, Q86. Three of them are the capture's three pre-existing parse `error=3`
queries, whose only "difference" is the temp filename embedded in the psql
error text. The real fire set is **Q77 and Q78**, unchanged.

## The 2026-09-21 blocker is gone

That no-go was Q78 at SF1 electing
`Nested Loop Left Join (cost=5494.86..1147565.07)` and not finishing inside
1800 s. With **M0145-0019a** landed it elects the hash join again:

```
->  Hash Left Join  (cost=16457.31..37717.11 rows=5731 width=240)
```

## SF1 execution A/B — values identical, Q78 unaffected

Private clone of the SF1 datadir, fresh server per arm (server age held
constant), knob arm:

| query | firewall ON | firewall OFF | checksum |
|---|---|---|---|
| Q77 | 5492 ms, 44 rows | **6373 ms**, 44 rows | `e2f12e6ef310f604` both |
| Q78 | 29608 ms, 100 rows | 29140 ms, 100 rows | `5e12c7e6baa093e8` both |

Q78 holds its shape and its ~29 s class. Values are byte-identical on both
queries. No timeout, no C-04a-class regression.

## Why it is STILL not executed

The task's first pass criterion is **"no NL-epsilon election"**, and Q77 has
one. With the firewall off, at both scales, one `Append` branch turns from

```
->  Hash Left Join  (cost=0.00..0.03 rows=1 width=136)
      Hash Cond: (s_store_sk = s_store_sk)
```

into

```
->  Nested Loop Left Join  (cost=0.00..0.06 rows=1 width=136)
      Join Filter: (s_store_sk = s_store_sk)
```

— a `rows=1` estimate, the equi-condition demoted to a `Join Filter`, and the
statement's top-level cost rising (SF0.25 `Limit` 10.65 → 11.58). It costs
**+881 ms, +16%** at SF1.

This is measurably benign and it is nothing like C-04a's 15 s → 327 s. It is
also, literally, the thing criterion 1 names, on the *degenerate* condition
`s_store_sk = s_store_sk`. Two readings are available — "a technicality on a
one-row branch, waive it" and "the criterion exists precisely because this
shape is how the 1.1M election started" — and choosing between them is the
owner's call, not the loop's, for three reasons:

1. executing means DELETING production code (`problemPairsOuterWithDerived`
   including its Semi/Anti arms, plus the diagnostic flag), which is not
   reversible under R3;
2. the owner's GO was conditioned on passing, and one criterion does not pass;
3. **M0145-0018's option choice is already awaiting an owner re-take** from
   loop #79, which measured options (b) and (c) to be the wrong targets.

So the measurement is delivered and the decision is left where it belongs.
Nothing in `problemPairsOuterWithDerived` was touched.

## What the owner needs to decide

Waive criterion 1 for the Q77 shape (+16% on a 5 s query, values identical,
Q78 unaffected) and execute the relaxation — or keep the firewall and close
0018 as permanently-a-cost-model-backstop, which is option 1 of the three this
document listed in 2026-09-21.

Artefacts: `tmp/m0145-0018-e1/` (both SF0.25 captures and the four SF1 plans).

---

# Execution (2026-09-23, loop #5)

The owner GO was executed. What changed:

- `internal/optimizer/relfromjoinlist.go`: `problemPairsOuterWithDerived`
  (LEFT/RIGHT/FULL **and** the Semi/Anti arms), `derivedFirewallEnabled`,
  the `searchOneProblem` decline site with its
  `traceSeamDecline("outer-over-derived", …)`, and `leafIsDerivedInput` all
  deleted. `leafIsDerivedInput` had no other reader — the
  `isSemiAntiSyntheticLeaf` flag's c8 consumer; the flag's live reader is the
  c11 fillable-coordinates rule, so the field stays.
- `internal/optimizer/flaglabels.go`: resolver deleted;
  `GOOPG_DERIVED_FIREWALL` moved to `flagProvenanceRetired` per the
  retirement convention — the order entry survives so older artefacts that
  carry it still decode, and the stamp reads `retired(M0145-0018)`.
  `scripts/planner-flags.env` regenerated.
- Tests: `outer_over_derived_test.go` deleted wholesale (every test pinned
  the removed function); the firewall-only pins in
  `semiantichain_test.go`, `joinsearch_c04c_trace_test.go`,
  `joinsearch_rightlink_test.go`, and the M0142 probe's consumer-#3 arm
  removed; admission, SJI, right-link and CTE-leaf pins kept.
- Stale comments rewritten where they described the firewall as live
  (`joinsearchseam.go`, `jointreepullup.go`, `cardinality.go`,
  `seam_leaf_admission_test.go`, `pullup_cte_leaf_test.go`,
  `in_unnest_sjinfo_test.go`, `joinsearchspine_test.go`). Notably
  `GOOPG_PULLUP_CTE_LEAF` now stands alone — its pairing requirement died
  with the firewall.

## Default-arm gate record

| gate | result |
|---|---|
| `go test ./internal/optimizer` | PASS |
| `RALPH_PRECOMMIT_SCOPE=units` | PASS |
| `tpch-spotcheck` | PASS (Q12=2, Q13=33; stamp shows `retired(M0145-0018)`) |
| SF0.25 sweep | PASS 96/96, MISMATCH=0, TIMEOUT=0 |
| SF0.25 plan channel | changed=2 — exactly Q77 and Q78 |
| seam census | `outer-over-derived` 3 -> 0 (both arms) |
| TPC-H acceptance arm | PASS, 24/24 value MATCH vs `tmp/m0122-0015-arm.txt` |
| SF1 Q77/Q78 spot-check | Q77 5.76 s, Q78 29.01 s, values identical |
| fire-set gate (M0145-0021a) | PASS — all fires PASS both arms on both corpora, `introduced=none` |

## What the post-removal plan actually did

The waived regression did not even recur. The E1 waiver covered Q77's
`rows=1` Append branch electing `Nested Loop Left Join (0.00..0.06)` with
`s_store_sk = s_store_sk` demoted to a Join Filter. On the executed tree the
same branch plans `Hash Left Join (cost=0.39..0.78 rows=12)`: the pulled
CTE leaves now reach the DP carrying `EstimateRows`-derived cardinalities
(rows=12/rows=60) instead of the `rows=1` epsilon, and with honest inputs
the hash join wins outright — the degenerate NL election the owner waived
no longer occurs. Q78 at SF1 keeps `Hash Left Join
(cost=16457.31..37717.11)`, zero `Nested Loop` nodes anywhere in the plan,
and finishes in its ~29 s class.

## Incident note (gate infrastructure, not product)

The first fire-set run wedged: the sf025 clone took ~11 min to bind its
listener (memory pressure from a concurrently-starting SF1 server),
tripping `jointree-parity-capture.sh`'s 120 s readiness loop; cleanup's
`goopg stop` ran before `postmaster.pid` existed and its `wait` then
blocked forever on the healthy server. Killed and re-ran with the
competing server down — clean PASS. Worth knowing: the capture's readiness
timeout assumes a fast clone start, and its cleanup can wedge on a
slow-starter.
