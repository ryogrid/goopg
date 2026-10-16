# M0146-0223 — TPC-DS Q39: a join keeps an ordering the grouping step can use

Status: landed 2026-10-17; TPC-DS Q39 matches PG at both scales. Evidence: `analysis/m0146/m0146-0223/`.

## 1. Record

TPC-DS Q39 (S9 pass at 472e41064, parallelism, SHAPE-DIFF at depth 4, at both scales; it persists under PG
statistics). In the `inv` CTE:

- PG: `HashAggregate → Hash Join (item) → Gather → Hash Join (warehouse) → Parallel Hash Join (inventory, date_dim)`,
  joining `item` above the Gather.
- goopg: `HashAggregate → Gather → Hash Join (warehouse) → Hash Join (item) → …`, joining `item` inside the workers.

The shipped corpora price `item_pkey`'s Index Only Scan at 478.29 against PG's 531.08. Under PG statistics goopg
prices it at 531.08 as well (the difference is `relallvisible`, which M0146-0215's owner import carries), and the
record persists, so the scan cost is not the cause.

## 2. Attribution (exact replica)

On the `pg_basebackup` replica (`probe/pg-exact-replica-q39-partial-verdicts.txt`), the 4-rel joinrel
(`inventory`, `item`, `warehouse`, `date_dim`) holds two partial candidates:

| partial path | cost | pathkeys | add_partial_path |
|---|---|---|---|
| Hash Join, `item` inner | 2721.00..25108.41 | none | rejected |
| Merge Join, outer {inventory, warehouse, date_dim}, inner `item_pkey` IOS | ..25201.06 | `inv_item_sk` | accepted |

Within the fuzz factor, better pathkeys win in `add_partial_path`, so the Merge Join evicts the cheaper Hash Join.
The Gather over it costs 27377.76 and loses to the serial plan with `item` joined above the Gather (27306.05).

goopg's trace (`probe/goopg-q39-4rel-trace-before.txt`) files the same Merge Join at 24568.34..25201.06, but with
`pathkeys=0`, and so it is dominated. The Hash Join partial survives and its Gather (27285.11) wins.

The pathkeys were dropped by `truncateUselessPathkeys`, which ported only `truncate_useless_pathkeys`' merging and
ordering arms. The CTE groups by `(w_warehouse_sk, i_item_sk, d_moy)`.
- `inv_item_sk` is in `i_item_sk`'s equivalence class but is not the leading group key, so the ordering arm (a common
  prefix with `query_pathkeys`) counts 0.
- Every class is closed inside the 4-rel joinrel, so the merging arm counts 0.

PG's `pathkeys_useful_for_grouping` counts the leading keys that are members of `group_pathkeys`, in any order, and
counts 1.

## 3. Change

- `deriveQueryPathkeySets` already built `group_pathkeys` and `distinct_pathkeys`. `resolveContext.setQueryPathkeys`
  now keeps both beside `query_pathkeys`. They ride to `searchCtx` the way `queryPathkeys` does: through
  `joinlistProblem`, then copied into GEQO tours.
- `pathkeyUsefulness` carries them, and `truncateUselessPathkeys` takes the maximum of four arms:
  - merging;
  - ordering;
  - grouping (`pathkeys_useful_for_grouping`);
  - distinct (`pathkeys_useful_for_distinct`).
  The last two use `leadingMembersOf`, which is `list_member_ptr` on canonical pathkeys: the same direction, nulls
  order and equivalence class.
- Class identity is `sameClass`, over every equivalence class in the search wherever it is enforced, because a PG
  pathkey names the class itself. `equivalentWithin`, the narrower test of whether this rel's rows are ordered
  alike, still serves the ordering arm and `pathkeysContainedInRel`.
- The set-op arm stays unported: goopg derives no `setop_pathkeys` (`querypathkeys.go`).

## 4. Measured

| corpus | before | after | movement |
|---|---|---|---|
| TPC-H | 22 / 22 | 22 / 22 | none |
| TPC-DS SF0.25 | 89 / 99 | **90 / 99** | Q39 MATCH |
| TPC-DS SF1 | 76 / 99 | **77 / 99** | Q39 MATCH |
| TPC-DS SF0.25, PG statistics | 93 / 99 | **94 / 99** | Q39 |

- CATEGORIES-EXCL-MATCH:
  - SF0.25: join-order 9 → 8, parallelism 3 → 2.
  - SF1: join-order 18 → 17, parallelism 6 → 5.
- No query regressed. The SF0.25 sweep reports one plan text changed (Q39).
- The SF1 census under PG statistics was not re-run; it needs a fresh SF1 clone.
- Executed values are unchanged:
  - the fired Q39 results are byte-identical at both scales;
  - the acceptance arm is value-identical.

**Pin:** `TestTruncateUselessPathkeysGroupingAndDistinct` covers:
- a group key matched by class, past the leading position;
- class membership outside the rel;
- a leading non-member;
- a direction mismatch;
- distinct members in any order.

Mutation-checked: dropping the grouping arm fails it.
