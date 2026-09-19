# M0142-0003i — Canonical TPC-H FK set landed in `build_schema_goopg.sh`; Q9 estimate collapse resolved

Task: `.ralph/fix_plan.md` **M0142-0003i**. Parent chain: **-0003e**
(diagnosed missing FKs as the schema gap), **-0003f** (first landing attempt;
unindexed validation scan blocked at `partsupp` scale), **-0003g**
(index-accelerated FK validation), **-0003j/-0003k** (cluster data-loss
investigation + owner-run recovery).

Status: **implemented** — see §"Verification on the private clone".

## What the task became

The owner's 2026-09-17 amendment re-scoped the task: never run DDL on the
shared `:65433`; put the FKs in `bench/tpch/build_schema_goopg.sh` (or a
post-load step) so every future rebuild gets them, verify on a private `55xx`
clone, and let the owner apply it to `:65433` at the next reload.

At this loop's start the restored `:65433` cluster carried all 8 PKs but
**zero** FK constraints (the owner-run restore came from
`preloss-clone-20260915`, which predates -0003f/-0003g's manual landings). So
the script block lands the full canonical 8-FK set, not just the 5 the
original task text named.

## Change

`bench/tpch/build_schema_goopg.sh`, after the HammerDB build and before the
reltuples/CHECKPOINT tail: eight `ALTER TABLE … ADD CONSTRAINT … FOREIGN KEY`
statements matching the PG 18.3 reference at `:65432` definition-for-definition:

- `nation_region_fk`, `supplier_nation_fk`, `customer_nation_fk`,
  `partsupp_part_fk`, `partsupp_supplier_fk`, `order_customer_fk`,
  `lineitem_partsupp_fk` (composite local order `l_partkey, l_suppkey`),
  `lineitem_order_fk` — **`DEFERRABLE`**, matching the reference.

HammerDB adds no FKs itself (confirmed: no repo script or HammerDB step
creates them; the reference set was historically added by an untracked manual
step). The count check that follows the DDL is what enforces: psql exits 0 on
per-statement errors without `ON_ERROR_STOP`, so a re-run prints "already
exists" per statement harmlessly, while a *partial* landing fails the script
with `fk_check: FAIL`.

## Verification on the private clone

Clone path: `pg_basebackup -X fetch` of the live `:65433`, served on `:5533`
(throwaway port, per the reference-cluster read-only rule).

- All 8 `ALTER TABLE … ADD CONSTRAINT` statements completed. Total wall time
  ≈ **6m49s** — the `lineitem` validations (6M rows) dominate; ~57µs/probe is
  consistent with -0003g's index-accelerated path (a heap-scan validation
  would not have finished).
- Raw `pg_constraint`: all 8 rows `contype='f'`; `lineitem_order_fk`
  `condeferrable=t`; `lineitem_partsupp_fk` `conkey={8,5}` /
  `confkey={1,2}` — composite order preserved.
- `pg_constraint` corpus diff vs PG `:65432`, restricted to the 8 user
  tables: **every PK and FK row identical** (name, type, conrelid, confrelid,
  conkey, confkey, deferrable flags). The only residual delta is PG's
  `contype='n'` NOT-NULL constraint rows — a separate catalog-modeling gap,
  not this task.
- Cosmetic gap noted in passing: `pg_get_constraintdef` returns empty
  strings for all constraint types (PKs too) — a general renderer gap, not a
  DDL/catalog failure.

### Q9 — the reason this task exists

-0003d's decisive finding: goopg's `lineitem ⋈ partsupp` composite-key join
estimated **2406 rows** (~2500× under), and every containing relset inherited
a ~146-row floor — because `get_foreign_key_join_selectivity` needs a
*declared* `pg_constraint` FK, not just a unique index (`-0003e`).

With the FK set present on the clone:

| join | goopg pre-FK (-0003d) | goopg now | PG 18.3 |
|---|---|---|---|
| `part ⋈ partsupp` | ~146-floor relsets | 15640 | 10102 |
| composite → `lineitem` | 2406 | **117313** | 75650 |

Same order of magnitude as PG everywhere; the ~2500× collapse is gone. The
plan now walks the FK-informed NL-index chain: `Gather > Partial HashAgg >
NL(Memoize→orders_pk) > NL(lineitem_part_supp_fkidx probe) > PHJ(supplier⋈nation)
> NL(Memoize→supplier_pk) > PHJ(partsupp⋈part)` — i.e. goopg now probes
`lineitem` by index like PG does instead of seq-scanning it. Topology still
differs from PG's (goopg memoize-NL-probes `orders`, PG hash-joins it; goopg
hashes `partsupp⋈part`, PG NL-probes it) — that is the separate -0003c
costing-ordering line, not this task's blockage.

Execution correctness: Q9 returns **175 rows on both engines** with identical
group keys; value-level differences are the recorded HammerDB-vs-dbgen data
composition divergence (-0003c), not introduced here.

## Caveats

- The clone's 6m49s for 8 constraints is acceptable for a build-time step but
  worth knowing: future `--reset` rebuilds pay it once per load.
- The live `:65433` still has **no** FKs — applying them there is the owner's
  call at the next reload, per the amendment.
- `make race-gate` remains red at HEAD for the unrelated
  `M-NIGHTLY-instrumentscope-race-fix` item (pre-existing, reproduced at base
  commit last loop).
