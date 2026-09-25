# Set-operation output type: porting `select_common_type`

Status: LANDED 2026-09-22 for PostgreSQL's numeric type category; pairs outside
it are declined and keep the previous behaviour (ledgered).
Kind: bug
Parent: none
Movement: none on the parity instruments — this is a declared-TYPE divergence,
which none of them measures.

## The defect

goopg resolved a set operation's output type to the **first member's**.
PostgreSQL resolves it with `select_common_type`
(`postgres/src/backend/parser/parse_coerce.c:1342`), driven from
`transformSetOperationTree`.

Measured, goopg vs PG 18.3:

| query | goopg (before) | PG |
|---|---|---|
| `SELECT 1 UNION ALL SELECT 2.5` | `bigint` | `numeric` |
| `SELECT 1::int2 UNION ALL SELECT 2::int8` | `smallint` | `bigint` |

The values were always right — `1`, `2.5`, `sum` = 3.5. Only the declared type
was wrong, which is why no row-count or value gate could see it. It is not
cosmetic: `\gdesc` (a Describe round trip, i.e. the wire RowDescription)
reported the same wrong type, so a typed client — JDBC, psycopg — was told
`int8` for a column carrying `2.5`.

## Root cause

Two pieces which together *are* the first-member rule:

- `SetOp.Output()` (`internal/optimizer/plan.go`) returns `n.Left.Output()`.
- `wrapSetOpBranchWithCasts` coerced only the **right** branch to the **left's**
  schema.

## The fix

`setOpUnifyBranches` resolves each column's common type across both branches
and coerces **both** to it. `SetOp.Output()` then becomes correct with no
change to `Output()` itself — coercing the left branch is exactly what the old
code omitted.

`setOpCommonTypeName` is the single decision point, so widening the modelled
set later is a one-function change.

## Why pairwise folding is sound — and what bounds the fix

`applySetOp` folds members **left-deep**, so goopg resolves pairwise where
upstream considers every member at once. That is only equivalent when the
category's implicit-coercion relation is a total order.

Within `TYPCATEGORY_NUMERIC` it is:

```
int2 < int4 < int8 < numeric < float4 < float8
```

float8 is additionally the category's *preferred* type, and since it is already
the maximum here, upstream's preferred-type arm needs no separate treatment.
So the pairwise fold converges on upstream's answer.

**This equivalence is the reason the fix stops at this category.** A category
whose coercion relation is not a total order would make the left-deep fold
order-dependent — which is the very defect being fixed, reintroduced in a
subtler form. Declining outside the modelled set keeps the previous behaviour
rather than trading a known divergence for an unknown one.

## Verification

Every expectation was measured on a live PG 18.3, not derived from the
algorithm. The test pins the planner's output **schema**, because the schema is
the only witness — the values were never wrong.

The reversed pair `float8 UNION ALL int2` is included deliberately as a paired
control: it is what distinguishes a real common-type resolution from a
positional rule, and it passes *even without the fix*, which is exactly why it
must sit beside a case that does not. Neutralising `setOpUnifyBranches` fails
exactly the four order-sensitive subtests and leaves the two controls green.

### The one plan delta, and a measurement trap

TPC-DS Q5's cost moved (`GroupAggregate` 14363.79 → 14595.00). The plan is
structurally identical — 66 lines, zero diff once cost text is stripped, row
estimates and widths unchanged — and the sweep reports `MISMATCH=0
CKMISMATCH=0`. The delta is the cost of the coercion Project the fix inserts on
a branch whose member types differ, i.e. the same coercion PostgreSQL inserts.

Worth recording: the first cost-stripped diff came back **empty** because the
awk range used `=== Q5` while the capture writes `===== Q5 =====`, so it
selected nothing. A vacuous "no diff" is indistinguishable from a real one
until the range is confirmed to match lines — re-running with a line count is
what exposed it.

## Still divergent (ledgered)

- **Cross-category pairs**: upstream raises 42804 *"types %s and %s cannot be
  matched"*; goopg still accepts the query. Rejecting needs a type-category
  mapping the optimizer does not have — the only `typcategory` table is
  `pgTypeCategoryForOID` in `internal/executor/pg18_user_catalog_rows.go`,
  keyed by OID rather than `catalog.Type` name.
- All-`unknown` members resolving to `text` (the tail of the same upstream
  function).
- The domain-preserving property of upstream's all-same fast path: goopg
  approximates it by type-**name** equality rather than by OID with
  `getBaseType` smashing.
