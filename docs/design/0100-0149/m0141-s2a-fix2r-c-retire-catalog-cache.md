# M0141-S2a-fix2r-c — retire the M0114 JSON catalog cache

Status: done 2026-10-10 (17e9db192). Parent: M0141-S2a-fix2r. Also
resolves the S2 defect M0146-0151 that this work found.

## Problem as filed

M0141-S2a-fix2r-a saw the planner size TPC-DS `customer.c_customer_id
char(16)` at 32 bytes. That is `get_typavgwidth`'s figure for a
`bpchar` with no typmod, although `pg_attribute.atttypmod` is 20. The task
was filed as "plan-schema types carry no typmod".

## Cause

The typmod was lost when the table was registered, not when the plan
schema was built.

- **What the cache was.** M0114's fast-start cache wrote
  `base/<db>/pg_goopg_catalog_cache.json` after a start that scanned the
  heap. On the **next** start, it registered user tables from that file
  and skipped `loadUserTablesFromHeap`.
- **What it kept.** Only each column's name, type **name**, NOT NULL and
  ordinal.
- **What a cache-hit start (the second restart of any cluster) lost:**
  - typmods, so `char(n)`, `varchar(n)` and `numeric(p,s)` had no length
    check, no blank padding on INSERT, and only the 32-byte default width;
  - `IsArray`, so an `int4[]` column was decoded as a scalar. A throwaway
    cluster returned `128` for `{1,2}` (wrong results, filed as
    M0146-0151);
  - identity, tablespace, database OID, reloptions and matview state.
- **Which clusters were affected.** Of the benchmark clusters, only TPC-DS
  SF0.25 carried a cache file. SF1 and TPC-H were already loading from the
  heap.
- **A correction to M0141-S2a-fix2r-a.** That task's "356 B" store-arm
  figure came from a second start of a private SF1 clone. The clone's first
  start had written a cache, and the second start read it. The first start
  measured 662 B, with typmods.

## PG behaviour

PG builds every user relation's descriptor from the system catalogs. Its
relcache init file (`pg_internal.init`, relcache.c
`load_relcache_init_file`) covers only the nailed system relations.

## Change

- **`open.go`** always runs `loadUserTablesFromHeap`. Before the scan it
  unlinks any cache file an older binary left behind.
- **`catalog_cache.go`** keeps only `UnlinkCatalogCache`, which is still
  called at DDL commit. The read and write paths and the JSON types are
  removed.

## A latent defect the cache hid (M0146-0152)

`TestCrashMidTransactionTableNotVisibleAfterRestart` failed once the cache
was gone. It runs `BEGIN; CREATE TABLE crash_ghost`, closes the runtime
without committing, then reopens it.

- **Why it passed before.** The second Open read the cache, which had been
  written before the CREATE.
- **What actually happens.** No transaction has committed since initdb,
  so the WAL holds no xact records. The reopen therefore takes the
  old-cluster upgrade branch, `clog.InitializeAsCommitted`. That stamps
  the crashed xid Committed, and the table reappears.
- **With any committed transaction first,** the crash-recovery sweep marks
  the xid Aborted and the table stays gone. The test now does that, which
  is the clog-filter path it documents.
- **The no-history edge** is filed as M0146-0152 (S2: an uncommitted
  CREATE resurrects).

## Verification

- **`TestColumnTypesSurviveRepeatedRestarts`** plants a retired-format
  cache, then checks across two restarts:
  - `char(16)` is `[16]`, `varchar(20)` is `[20]`, `numeric(7,2)` is
    `[7 2]`, and `int4[]` has `IsArray`;
  - the cache file is removed.
  It fails on HEAD at restart 2.
- **Throwaway cluster, two restarts.** The array column, the `char(5)`
  length error, padding and a group-by width of 76 all match a scratch
  PG 18.3.
- **TPC-DS fire set.**
  - SF0.25: all 99 plan texts change, because widths now carry typmods
    (Q4's `year_total` HashAggregate goes 260 → 638, as SF1 already had).
    Shapes, categories and matches (56) are unchanged, and no timeouts are
    introduced.
  - SF1: no fires.
- **TPC-H.** Plans are identical; the acceptance arm matches on values.
- **SF0.25 sweep.** PASS=99 on values; the plan text changed for all 99.
- **Other gates.** ea-ratchet (1) passes, and the regress A/B shows only
  the join flap.

## Not covered (filed)

- **M0146-0152:** the first transaction after initdb, if it never commits,
  survives a restart.
- **M0146-0153:** a GENERATED ALWAYS identity column accepts an explicit
  value with no OVERRIDING clause (PG: `cannot insert a non-DEFAULT value
  into column`). This happens with no restart involved. It contradicts
  ledger row M0134-0029, which says plain INSERT raises the error.
- **M0146-0154:** after a clean restart an identity sequence resumes 32
  values ahead (34 where PG gives 2). goopg restores the every-32-nextval
  snapshot as PG does only after a crash.
