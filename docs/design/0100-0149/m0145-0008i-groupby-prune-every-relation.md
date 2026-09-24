# M0145-0008i: remove_useless_groupby_columns over every relation

Status: **LANDED 2026-09-25**. Task: `.ralph/fix_plan.md` M0145-0008i (Kind:
impl, Parent: M0145-0008d). Evidence: `analysis/m0145/m0145-0008i/`.

## Defect

`pruneUselessGroupByColumns` (`internal/optimizer/planner.go`) implemented
only the single-relation arm of PG's `remove_useless_groupby_columns`
(`postgres/src/backend/optimizer/plan/initsplan.c:412`). It declined whenever
the FROM clause held more than one catalog table. PG decides each rtable
relation independently and unions the drops (`surplusvars[k]`). So TPC-H
Q18 grouped on five keys where PG groups on two (`c_custkey, o_orderkey`),
and Q10 on seven where PG groups on two (`c_custkey, n_name`). After
M0145-0008d made the ORDER BY prefix reorder the group clause, the five keys
also made Q18 elect GroupAggregate where PG keeps HashAggregate + Sort.

## Fix

- `pruneUselessGroupByColumns` now runs per base-relation binding and unions
  the drops. The per-relation decision moved unchanged into
  `uselessGroupByKeyForBinding`: at least two grouped columns of the
  relation, the smallest non-deferrable, non-partial, non-expression unique
  key that is a proper subset of them, its NOT NULL (or NULLS NOT DISTINCT)
  check, the inheritance-parent skip, and the partition-key coverage guard.
- **Base relations only.** PG iterates `rtekind == RTE_RELATION`. Subquery,
  CTE, VALUES and function-scan bindings carry a synthesised
  `catalog.Table` with OID 0, and a view binding carries the view. Both are
  skipped explicitly, so no synthesised table can reach
  `IndexesOnTable`. The old comment claiming such bindings "carry no
  catalog table" was wrong. The old single-relation guard only happened to
  cover them, by declining any multi-binding FROM.
- **No outer-join guard, as in PG.** A NULL-extended key still determines
  its NULL-extended dependents.

Pinned by `TestGroupByPruneAcrossJoinedRelations` (executor). It checks the
inner-join and LEFT JOIN Group Key (`gc.ck, go.ok`) and rows against PG
18.3 output captured on a scratch cluster, and that a CTE named like a base
table does not borrow the table's primary key.

## Measured

Fire-set gate, HEAD `68b13597c` vs staged.

- **TPC-H:** Q10 and Q18 move. Both now print PG's exact Group Key:
  - Q18 elects PG's `Sort / HashAggregate (c_custkey, o_orderkey)` in place
    of the GroupAggregate over a five-key sort, and leaves
    `sort-strategy`;
  - Q10 leaves `rendering`.
  - `CATEGORIES-EXCL-MATCH` sort-strategy 9→8, rendering 3→2. Values are
    identical and `match` is unchanged at 3/22.
- **TPC-DS:** Q39 and Q64 group-key text changed at SF0.25 and SF1, with no
  category change and no introduced timeout.
- **Timing.** The fire-set execution showed Q10 at 9.5 → 22.2 s. That was
  nightly-batch noise: the arms ran minutes apart under a varying load. An
  alternating A/B through the acceptance arm (base, cand, base, cand under
  the same load) reads Q10 9.06/8.68 → 8.60/8.48 s and Q18 12.45/11.60 →
  11.68/12.20 s. No regression.

## Gates (staged tree)

- units PASS. One earlier run failed on
  `TestStandbyControllerPromoteSignalTriggersPromote`, a 1.5 s promotion
  deadline missed under nightly load. It passed 3/3 in isolation, and the
  re-run was clean.
- `tpch-spotcheck` PASS.
- fire-set at SF0.25, SF1 and TPC-H: `introduced=none`.
- `tpcds-sf025 sweep` 96/96, `changed (2): Q39 Q64`.
- acceptance arm 24 MATCH on values.
- ea-ratchet 52/52.
- The sweep, the acceptance arm and the TPC-H fire-set execution ran with
  `FORCE=1`, because the nightly CI batch was live: valid for values, not
  timing.

Movement: none (no category beyond ±3; match counts unchanged).
