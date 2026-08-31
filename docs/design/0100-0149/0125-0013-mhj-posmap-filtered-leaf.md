# M0125-0013 — the MHJ leaf that was skipped twice over

*Status: superseded — MHJ retired (M0127); see [leftdeep-joins/](leftdeep-joins/). Originally accepted. Branch `tpcds-fix2`. Closes TPC-DS Q47
(`MISMATCH goopg=0 oracle=100` → 100 rows).*

## 1. The symptom, and why it hid for so long

Q47 returned **0 rows** where PG returns 100. RC-1b (`5db0a067`) had already
made Q47's CTE body exactly correct — 661,185 rows, equal to PG — so the
remaining defect was known to sit *above* the CTE, in the `v1`→`v2`
windowed self-join layers that until RC-1b had never received non-empty
input. The fix_plan item said to start there.

It is not there. The defect is in the CTE body after all — but in a half of
it that the RC-1b measurement never looked at.

The reason it hid is worth stating plainly, because it is the same reason
D6a exists (M0124-0006): **the row count was right.** The 4-way join

```sql
from item, store_sales, date_dim, store
where ss_item_sk = i_item_sk and ss_sold_date_sk = d_date_sk
  and ss_store_sk = s_store_sk
  and ( d_year = 2000 or (d_year = 1999 and d_moy = 12)
                      or (d_year = 2001 and d_moy = 1) )
```

produces **332,240 rows on goopg and 332,240 on PG**. Every join predicate
resolved correctly and every filter was applied correctly. Only the
*projection* read the wrong columns. RC-1b counted rows, the count agreed,
and the query moved on.

What the projection actually returned:

| requested | goopg returned | which is |
|---|---|---|
| `i_category` | `1, 2, 4, 7, 8, 10` | `s_store_sk` values |
| `d_year` | `Williamson County` | `s_county` |
| `d_moy` | `31904` | `s_zip` |
| `s_store_sk` | `2451572` | a `d_date_sk` |
| `d_date_sk` | `Unknown` | `s_company_name` |
| `i_item_sk` | `TN` | `s_state` |

`GROUP BY i_category` therefore grouped on `s_store_sk` and produced 6
groups instead of 11; `GROUP BY` on 6 columns and on 4 columns both
collapsed to the *same* 29,617 groups. Downstream, `rank() over (partition
by i_category, i_brand, s_store_name, s_company_name order by d_year,
d_moy)` partitioned on columns that were unique per row, so **`rn` was 1
for every one of the 29,617 rows** (PG: 1..14). `v2` joins `v1` to itself on
`v1.rn = v1_lag.rn + 1`, i.e. `1 = 2` — empty, for every row. Hence 0.

So the window layer was never broken. It was fed a permutation.

## 2. Root cause

`buildBindingsPosMap` (`internal/planner/bushy.go`) builds the map that
translates a ColumnRef's **FROM-cumulative** index into the **actual
plan-output** index, after `rewriteMultiWayChain` has packed base joins
into a `MultiHashJoin` and OID-sorted its columns. Its `collect` walker
has an arm per node kind. The `*MultiHashJoin` arm read:

```go
case *MultiHashJoin:
    for _, t := range x.Tables {
        switch s := t.(type) {
        case *SeqScan:  /* record entry; off += width */
        case *IndexScan: /* record entry; off += width */
        }
    }
```

An MHJ table is **not always a bare scan**.
`pushSingleSourceFiltersIntoMHJTables` replaces `mh.Tables[i]` with
`&Filter{Child: <scan>}` whenever it pushes a single-source conjunct into
that input (`mhj_input_rewrite.go:803`). Q47's multi-column OR disjunction
is exactly such a conjunct, and it is pushed into the `date_dim` leaf.

For a `*Filter`-wrapped table the switch above matched **nothing**, and did
so silently. That is two distinct corruptions from one omission:

1. **No `scanEntry`** — `date_dim` is absent from `scanMap`, so the posMap's
   closure falls through to `return oldIdx` and every `date_dim` column
   keeps its FROM-cumulative index.
2. **`off` is never advanced** — the damaging half. Every table to the
   *right* of the wrapped one is registered at an offset short by the
   wrapped table's width.

Both halves are visible in the table above, and the arithmetic closes
exactly. FROM order is `item(22), store_sales(23), date_dim(28), store(29)`
→ offsets 0, 22, 45, 73. MHJ order is `date_dim, store, store_sales` → true
offsets 0, 28, 57. With `date_dim` skipped and `off` left at 0, `store` is
registered at 0 and `store_sales` at 29:

- `d_year` = 45+6 = **51**, `date_dim` not in scanMap → stays 51 → MHJ index
  51 = store-local 23 = `s_county` ✓
- `d_moy` = 45+8 = **53** → stays 53 → store-local 25 = `s_zip` ✓
- `s_store_sk` = **73** → scanMap[store]=0 → 0 → date_dim-local 0 =
  `d_date_sk` ✓
- `d_date_sk` = **45** → stays 45 → store-local 17 = `s_company_name` ✓
- `s_store_name` = 73+5 = **78** → 0+5 = 5 → date_dim-local 5 =
  `d_quarter_seq` (observed `402`) ✓
- `s_company_name` = 73+17 = **90** → 0+17 = 17 → date_dim-local 17 =
  `d_weekend` (observed `N`) ✓
- `i_item_sk` = **0** → item's IndexScan entry sits after the MHJ at
  29+23 = 52 → store-local 24 = `s_state` (observed `TN`) ✓
- `ss_item_sk` = 22+2 = **24** → scanMap[store_sales]=29 → 31 →
  store-local 3 = `s_rec_end_date` (observed blank — NULL for current
  stores) ✓

Eight of eight. There was no second mechanism to look for.

### Why the *inner* remap was fine

`pushSingleSourceFiltersAfterRemap` runs at `planner.go:1144`, i.e. **after**
`remapWithBindings` (1131) and **before** `remapTopProjection` (called from
`planSelect`). It creates the `*Filter` wrapper *between* the two remaps. The
first remap saw bare scans and got everything right — which is why the join
predicates worked and the row count was correct. Only the second remap, the
one that fixes up the top projection and sort keys, tripped over the wrapper.

That ordering is deliberate and correct: RC-1b moved the push to *after* the
remap precisely because attribution by index range is only meaningful in
MHJ-output coordinates. The bug is not the ordering; it is that a downstream
consumer of the plan did not expect the shape the push leaves behind.

## 3. The fix

One arm, replaced by a recursive call:

```go
case *MultiHashJoin:
    for _, t := range x.Tables {
        collect(t)
    }
```

`collect` already handles `*SeqScan` and `*IndexScan` identically to the
deleted code (M0062-0002 alias preservation included), already has a
pass-through `*Filter` arm that descends without advancing `off`, and
already has a `default:` arm that sets `declined = true` on anything it
cannot classify — abandoning the whole remap rather than applying it with a
wrong offset.

That `default:` arm is the point. It was added by **RC-2** for exactly this
failure mode — "an unhandled node used to fall through silently, leaving
`off` un-advanced — a wrong answer or an out-of-range panic,
unconditionally". The hardening was applied to the top-level walker and
never to the MHJ loop nested inside it, which kept its own private,
unhardened switch. Routing through `collect` is what makes the two agree,
and is why the fix is a deletion rather than a third enumeration to keep in
sync.

This is the project's recurring **sibling-paths** defect (Hard-won Rule #2)
in a new position: not encode↔decode or fast-path↔interpreted, but
*outer walker ↔ its own nested inner walker over the same node kinds*.

## 4. Relationship to M0125-0008

Both are "a plan node's published column layout disagrees with what its
child actually produces", both were caused by `rewriteMultiWayChain`'s
in-place OID sort, and both survived because widths matched so nothing
detected the permutation. They are not the same defect: -0008 was a *stale
cached copy* of a layout (fixed by deriving instead of caching), this one is
a *walker that could not see a leaf* (fixed by removing the private switch).
The shared lesson is narrower than "layouts go stale": **a node kind added
in one pass becomes an unhandled shape in every pass that pattern-matches on
node kinds**, and the passes that pattern-match are not co-located with the
pass that introduced the shape.

## 5. Verification

Regression tests (`internal/planner/mhj_posmap_filtered_leaf_test.go`), both
verified RED with the fix reverted:

- `TestBuildBindingsPosMapSeesThroughFilteredMHJLeaf` — FROM order A,B,C,
  MHJ order C,A,B, C wrapped in a `*Filter`. All five index assertions fail
  pre-fix, each with the exact pre-fix value recorded in the failure message
  (0→0, 1→1, 2→2, 5→5, 8→8: the identity map, i.e. no remap at all).
- `TestBuildBindingsPosMapBareMHJLeavesUnchanged` — same layout, all leaves
  bare. **Passes pre-fix and post-fix**, pinning that the change is a strict
  generalisation and not a re-derivation of every existing MHJ plan.

Measured against the PG 18.3 SF0.5 oracle, all previously divergent, all now
exact:

| probe | goopg before | goopg after | PG |
|---|---|---|---|
| join cardinality | 332240 | 332240 | 332240 |
| `GROUP BY` 6 cols | 29617 | **54915** | 54915 |
| `GROUP BY` 4 cols | 29617 | **5616** | 5616 |
| `GROUP BY i_category` | 6 groups | **11** | 11 |
| `v1` rows / `rn` range | 29617 / 1..1 | **54915 / 1..14** | 54915 / 1..14 |
| **Q47 rows** | **0** | **100** | 100 |

Gates: planner + executor package suites PASS; `RALPH_PRECOMMIT_SCOPE=units`
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35);
`make plan-diff LABEL=tpcds-round2-head` **22/22 MATCH** (plan *shapes* are
untouched — this fix moves indices, not nodes); SF0.5 subset probe over the
`avg_monthly_sales` window family plus M0125-0008's three closed queries —
`PASS=7 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1`, with Q16/Q94/Q95
returning byte-identical checksums to the previous loop
(`40dbec0df91d2438`, `04afc1b69831a5ea`, `e498634c02595c29`).

Q47's TIMEOUT in that probe is the harness's 300 s cap on a host running the
nightly CI batch at load ~10; run standalone in the same conditions Q47
completes and returns **exactly 100 rows**.

## 6. Deferred

- **The full 99-query SF0.5 gate** — now three engine commits deep. See the
  ledger row; the nightly batch held the host again.
- **Q47's STEP-0 runtime question** (set A `OK 17 s` → HEAD `OK 142 s`,
  8.4×) is NOT settled by this loop and no verdict was written into either
  disagreeing document. It cannot be: every timing available here was taken
  under the nightly batch. What this loop *does* establish is structural and
  favours the RC-1b reading — `v1` genuinely produces 54,915 groups where it
  previously produced 29,617 mis-grouped ones, and `v2` self-joins that
  three ways over a `rank()` that now has 14 distinct values instead of 1,
  so the post-fix runtime is doing strictly more real work than any
  pre-RC-1b measurement. That is an argument, not a measurement, and the
  ledger row keeps it open.
- **`NestedLoopIndexJoin.Output()`** remains as M0125-0008 left it.
