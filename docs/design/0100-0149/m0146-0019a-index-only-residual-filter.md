# M0146-0019a — index-only paths keep a residual Filter

Status: done 2026-10-05. Parent: M0146-0019. Evidence:
`analysis/m0146/m0146-0019a/`.

## PG mechanism

- `build_index_paths` (indxpath.c) builds one path per index. It is
  index-only whenever `check_index_only` holds, i.e. the index covers every
  column the query reads from the relation. The clauses the index binds are
  its `indexclauses`; every other restriction stays in the scan's qpqual and
  prints as the Index Only Scan's `Filter:`. With no bindable clause it is
  a full index scan. TPC-H Q16: `Parallel Index Only Scan using partsupp_pk`
  with `Filter: (NOT (ANY (ps_suppkey = (hashed SubPlan 1).col1)))`.
- An uncorrelated `= ANY` sublink whose result fits in hash_mem is a hashed
  SubPlan (`build_subplan` sets `useHashTable`; AlternativeSubPlan is
  generated only for a correlated EXISTS). `cost_subplan` charges the plan's
  total cost plus `cpu_operator_cost` per row once, at startup, and per
  evaluation only the test expression.

## What goopg did

- `addIndexOnlyPaths` refused any leaf with a local qual its equality-prefix
  clauses did not consume (M0145-0029 slice 3). The leaf's predicate is
  written against the full leaf schema, and the narrowed scan emits only
  the covered columns.
- `qualEvalOps` priced every uncorrelated ANY as the plain SubPlan (half the
  run cost per call). Q16's `NOT IN` cost about 84 operators per row, so any
  path evaluating it on 800k partsupp rows lost to the nested loop that
  evaluated it on about 2 rows per probe.

## Change

- **Path (`pathindexonly.go`):** `indexOnlyLeafClauses` replaces
  `consumingIndexClauses`.
  - The equality-prefix clauses are index quals, possibly none (a full
    index-only scan). The unused conjuncts are the residual.
  - The residual is admitted when every column it reads is covered and no
    sublink in it is correlated (`indexOnlyResidualAdmissible`).
  - The same NULL-key rule applies (`indexUnboundKeysNotNull`).
  - `plainRestrictionBindsOnlyPrefix` keeps PG's one-path-per-index rule. If
    the plain restriction producer would bind more (a range after the
    prefix, a leading SAOP or range, a skip run), the index-only path is not
    built and the plain path keeps the index.
- **Plan (`createplanindex.go`):** `rewrapLeafDroppingRemapped` reinstates
  the residual as the Index Only Scan's Filter. Each ColumnRef is re-based
  from the leaf schema onto the covered column it names, and the build
  panics if a column is not covered.
- **Hashed SubPlan cost (`qualevalcost.go`):** `inSubPlanHashed` follows
  `subplan_is_hashable` and `build_subplan`'s other conditions: no
  correlation, a single-operand plain equality, and a result within the
  boot hash_mem. Such an ANY costs its load at startup and only the
  comparison per row. The old unit test pinned the plain pricing on the
  AlternativeSubPlan argument; it now pins PG's.
- **Layout (`createplanjoin.go`):** `baseRelLayout` took output position i
  as binding position i whenever the rebuilt leaf was as wide as the table.
  An index-only scan covering every column emits them in index order, so
  regress `create_index`'s `onek_with_null` (index `(unique2, unique1)`)
  tripped the boundary-identity assertion. The identity now also requires
  equal names; otherwise positions are matched by name, as for a narrowed
  scan.

## Result

- **TPC-H Q16:** HEAD's Nested Loop (8.36M) becomes PG's Parallel Hash Join
  (38.9k; PG 37.1k).
  - TPC-H CATEGORIES-EXCL-MATCH: join-order 9 → 8, join-method 4 → 3.
  - Q16's last category is scan-type: the partsupp leaf is a Parallel Seq
    Scan, where PG's is a Parallel Index Only Scan. goopg's calibrated
    `indexProbeCostMultiplier` = 2 (cost_funcs.go, owner-sanctioned, slated
    to return to 1.0) doubles the IOS index-page charge: 38.6k serial
    against PG's 26.7k. With `GOOPG_INDEX_PROBE_MULT=1` Q16 is PG's exact
    shape (IOS 18.8k vs PG 19.2k), and its rows match `:65433`.
  - Arm 24/24 MATCH; Q16 runs 0.63 s on both plans.
- **TPC-DS:** no plan change at either scale. PG's Q23 index-only scans
  (`customer_pkey`) carry no Filter, so they are not this shape.
- **Regress A/B (14 files):**
  - create_index's correlated-subplan inner becomes PG's `Index Only Scan`,
    still with a Filter where PG has an Index Cond.
  - union's first EXCEPT becomes PG's `Index Only Scan using tenk1_unique2`
    with `Filter: (unique2 <> 10)`.
  - Every other difference is a known flap.
- **Witness:** `TestIndexOnlyScanKeepsResidualFilter` covers a hashed NOT IN,
  an operator residual, and the permuted full-width index. Its plans and
  values are PG 18.3's.

## Open (ledgered)

- An index-only path binds only an equality prefix: there is no SAOP,
  range or skip lowering.
- `inSubPlanHashed` reads the boot hash_mem (4MB × 2), so a session
  `work_mem` / `hash_mem_multiplier` is not seen.
- Scan cost consumers drop a qual's startup (`_, ops :=
  conjunctsEvalOps`), so the hashed SubPlan's load cost is not in the scan's
  startup as in PG (350 on Q16's IOS). It is equal on both scan rivals, so
  no access-path choice turns on it.
- A correlated outer-param key is still not an index-only Index Cond
  (regress create_index `t2.thousand = (t1.tenthous + 1)`). The probable
  cause is the int8 literal typing of M0146-0062.
- The arm's online clone of `:65433` reads `relallvisible = 0`, because
  goopg writes the visibility map only at clean shutdown (`SaveVM`). Filed
  as M0146-0063.
