# M0125-0008 — a Semi/Anti join that published a layout it never produced

**Status:** landed 2026-07-30 · branch `tpcds-fix2`
**Closes:** `M0125-0008` (TPC-DS Q16, Q94) and — unexpectedly — `M0125-0023` (Q95)
**Touches:** `internal/planner/plan.go` (`Join.Output`)

## 1. The symptom that named the task

TPC-DS Q94 asks for web orders that were shipped from more than one
warehouse and never returned:

```sql
… and exists     (select * from web_sales  ws2 where ws1.ws_order_number = ws2.ws_order_number
                                              and ws1.ws_warehouse_sk  <> ws2.ws_warehouse_sk)
  and not exists (select * from web_returns wr1 where ws1.ws_order_number = wr1.wr_order_number)
```

Each correlated subquery was correct **alone**. Together they were not.
At SF=0.5, post-`M0125-0007`:

| query | goopg | PG 18.3 |
|---|---|---|
| Q16 | `63 \| 319602.45 \| -91294.46` | `23 \| 93334.17 \| -35323.69` |
| Q94 | `7 \| 10534.30 \| 7178.64` | `2 \| 5037.18 \| 1067.82` |

The interesting part was never the wrong number, it was the *direction*.
`base AND p AND q` returned **more** rows than `base AND q` alone. ANDing a
conjunct can only ever remove rows, so this was not a mis-estimated
predicate or a lost `<>` — one of the two conjuncts had stopped filtering
altogether, and the result was not even a subset of what the anti-join by
itself admitted. That non-subset signature is what makes this a structural
bug rather than an arithmetic one, and it is what the regression test now
asserts directly.

## 2. Reproducing it small

A five-table fixture with four orders reproduces it exactly (fixture and
PG-derived expectations in `internal/executor/semi_anti_conjunction_test.go`):

| order | two warehouses? (`EXISTS`) | absent from returns? (`NOT EXISTS`) | verdict |
|---|---|---|---|
| 1 | yes | yes | **keep** |
| 2 | yes | no | drop |
| 3 | no | yes | drop |
| 4 | no | no | drop |

The isolation matrix, goopg before the fix vs PG 18.3:

| arm | goopg (3+ base tables) | PG 18.3 |
|---|---|---|
| base | `6 \| 4` | `6 \| 4` |
| `+ EXISTS` | `4 \| 2` | `4 \| 2` |
| `+ NOT EXISTS` | `3 \| 2` | `3 \| 2` |
| **both** | **`4 \| 2`** | **`2 \| 1`** |

`both` came back byte-identical to `EXISTS` alone: the `NOT EXISTS`
conjunct contributed nothing. Reversing the two conjuncts in the SQL moved
the failure — `both` then equalled `NOT EXISTS` alone. **Whichever
semi/anti join ended up on top was the one that stopped filtering.**

The other end of the bisect was just as sharp. With one or two base tables
the answer was correct; from **three** base tables on it was wrong. Three
is the threshold at which `rewriteMultiWayChain` packs the base joins into
a `MultiHashJoin`.

## 3. Root cause

A Semi/Anti join emits the **outer (Left) row only** — the inner side is a
membership test, not a source of columns. All three construction sites in
`unnest.go` encode that by setting the node's cached `schema` to a *copy*
of `Left.Output()`, and `runJoinSearchBelowPinned` (predp.go) re-copies it
after join-order search moves the subtree underneath.

The pipeline then continues (`planner.go`):

```
  unnestSubqueriesInPlan          → pins the semi/anti spine
  runJoinSearchBelowPinned        → DP below the spine; refreshes spine schemas ✔
  rewriteMultiWayChain            → packs ≥3 base joins into an MHJ and
                                    OID-SORTS its output columns, IN PLACE ✘
  remapExprRefsToMHJ / remapWithBindings
                                  → reresolveJoinByName re-binds join keys BY NAME
```

`rewriteMultiWayChain` sorts the MHJ's columns by catalog OID
(`bushy.go`, "so the output schema is in FROM-clause order"). That rewrites
the layout *below* the pinned spine after the spine's schemas were last
refreshed — so each Semi/Anti node's cached copy silently became a stale
**permutation** of the layout its child now produces.

Nothing detects this, because the widths still match. What happens next is
the actual damage: `reresolveJoinByName` re-resolves a join's keys by NAME
against `j.Left.Output()`. For the *upper* semi/anti join, `Left` **is** the
lower semi/anti join — so it resolved against the phantom layout:

```
  ANTI.LeftKey  = ord → index 2      (position of `ord` in the STALE  [ca, o, d])
  runtime layout                      [o, d, ca] — index 2 is `dsk`
```

The anti-join then hashed `dsk` values against `wr_order_number`, matched
nothing, and — being an anti join — passed **every** probe row through.
The conjunct was still in the plan; it just could not reject anything.

The lower join escaped because its own `Left` is a real node whose
`Output()` is always live. Only a semi/anti join *stacked on another one*
inherits the lie, which is exactly why both affected queries pair two
subqueries over a single outer relation.

## 4. The fix

The cache was never carrying information: every writer set it to
`Left.Output()`, and the invariant is a definition, not a choice. So derive
it (`internal/planner/plan.go`):

```go
func (n *Join) Output() Schema {
	if n.Type == JoinTypeSemi || n.Type == JoinTypeAnti {
		if n.Left != nil {
			return n.Left.Output()
		}
	}
	return n.schema
}
```

Four passes previously had to remember to refresh that copy, and a fifth
(`rewriteMultiWayChain`) did not know it existed. Deriving the layout makes
the invariant structural, so a future pass that rewrites a `Left` child in
place cannot re-open this. `schema` remains the source of truth for every
other join type, where it legitimately holds the *merged* layout.

Two things this deliberately does **not** do. It does not widen a semi/anti
join to the merged layout — the `reresolveJoinByName` guard against that
(added for Q21, where a widened SemiJoin schema silently zeroed ~411 rows)
stays exactly as it was. And it does not touch `NestedLoopIndexJoin`, which
has the same shape of cache; see §6.

## 5. Acceptance

Measured, not projected.

- **TPC-DS SF0.5, against the git-tracked PG 18.3 oracle** — Q16
  `ck=40dbec0df91d2438`, Q94 `ck=04afc1b69831a5ea`: both were CKMISMATCH,
  both now PASS on the oracle's exact value checksum.
- **Q95 (`M0125-0023`) also PASSES**, `ck=e498634c02595c29`. This was filed
  as a separate defect on the reasoning that "Q95 contains no `EXISTS`".
  That reasoning tracked the keyword rather than the mechanism: Q95 has
  **two `IN (subquery)` predicates over one outer relation**, and
  `IN`-unnest produces `JoinTypeSemi` — the identical stacked shape. It
  under-counted rather than over-counted simply because the neutered
  conjunct was a semi join (which then admitted everything) rather than an
  anti join.
- **Subset sweep** over all 13 SF0.5 queries containing `EXISTS`/`IN
  (SELECT)`: `PASS=9 (6 ck-verified) MISMATCH=0 CKMISMATCH=0 ERROR=0
  TIMEOUT=4`. The four timeouts (Q10, Q14, Q35, Q69) are the pre-existing
  M0125 timeout class — each is TIMEOUT in prior sweeps
  (`sweep-20260729-181319`, `-210715`, `-221359`, `-225808`) and none is
  introduced here.
- **`make plan-gate`** vs `plan_snapshots/tpcds-round2-head.txt`: all 22
  TPC-H plans MATCH — no plan-shape change.
- **`scripts/tpch-spotcheck.sh`**: Q12=2, Q13=35 (canonical).
- **Regression tests**, both verified to go RED with the fix reverted:
  - `internal/executor/semi_anti_conjunction_test.go` — the four-arm
    isolation matrix at 2 / 3 / 4 base tables against PG-derived values,
    plus the monotonicity invariant (`base+p+q ⊆ base+q`) asserted
    directly. The 2-table arm is retained as the control that was already
    correct, so the test pins the *threshold*, not just the symptom.
  - `internal/planner/semi_anti_schema_invariant_test.go` — walks the
    FINAL plan (post-DP, post-MHJ, post-remap) and asserts every Semi/Anti
    join publishes exactly `Left.Output()`, for both conjunct orders and
    all three base widths.

Still owed independently of this change: one full 99-query SF0.5 gate run
on a quiet host. It was not taken here because the nightly CI batch held
the host (load average 19), which would have manufactured false TIMEOUT
verdicts.

## 6. Deferred

`NestedLoopIndexJoin.Output()` (`plan.go:707`) returns its cached `schema`
unconditionally, and `reresolveNLIKeysByName` sets that cache to a snapshot
copy of the outer schema for semi/anti NLI (`bushy.go:2749`) — the same
staleness class this document closes for `*Join`. It is refreshed by
`reconcileNLILayout`, but that pass is gated on `costDrivenJoinOrder` and
so does not run on the default path. It is left alone deliberately: the
surrounding comments record that remapping NLI keys empirically broke
TPC-H Q9's chained-NLI shape and Q21's anti-NLI keys, so this needs its own
reproducer before being touched. Ledger row filed 2026-07-30.

## 7. Why this recurred

This is the "sibling paths must change together" failure (Hard-won Rule
\#2) in its cache form: a value derived from another node, copied at four
sites, with no assertion tying the copy to its source. The general lesson
is narrower than "refresh the cache" — it is that a field which every
writer sets to the same derived expression should be that expression.
