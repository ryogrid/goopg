# M0122-0012 C59 — the int lane's open-addressing hash tables (PG simplehash)

Status: slices 1 and 2 landed 2026-10-28 (loops #254, #255). Slice 3 is open (ledgered).

## Problem

goopg keeps its int-keyed hash tables in Go maps:

- the hash join's int lane, `joinOp.lazyIntHash map[int64][]Row`;
- the hashed aggregate's int lane, `igroups map[int64]*groupRuntime`.

Go's map is a swiss table. A lookup hashes the key, reads a group's 8-byte control word, then reads a slot that
usually sits in another cache line. On a table larger than the caches, that is two misses before the rows
themselves are touched.

TPC-H Q13 is the case in the census-20261028c top 10. It is fully serial: a Hash Right Join probes 150 K customers
with 1.48 M orders rows, and then a HashAggregate groups by c\_custkey. By cycles:u, `runtime.mapaccess1_fast64` is
17.5 % of the query: 66 % of it is the join probe and 29 % the group lookup.

A micro-benchmark at Q13's shape (150 K int keys, 1.5 M random probes) measures about 45 ns per Go-map probe against
about 11 ns per probe of a flat linear-probe array.

## PG

The hash join probes a bucket array: `ExecScanHashBucket` (nodeHash.c) reads `hashtable->buckets[bucketno]` and
walks its chain. Grouping tables are simplehash.h instances (`TupleHashTable`, execGrouping.c): a power-of-two
array of entries probed linearly, one array slot per step, with no control-word indirection.

## Design

`intProbeIndex` (internal/executor/int\_probe\_index.go):

- **Entries.** A flat array of `{key int64, rows []Row}`, where a nil `rows` marks an empty slot.
- **Capacity.** A power of two at load ≤ 2/3.
- **Hash.** Fibonacci hashing (`key × 0x9E3779B97F4A7C15 >> shift`), then linear probing.
- **Lookup.** One slot read in the common case, and the slot holds the rows slice header itself.

### Slice 1 (landed): a frozen probe index for the hash join

The map stays the source of truth for about 25 sites: the build, batching and spill, the shared parallel build, the
outer-join fill, and demotion to the string lane. Only the per-row probe in `joinOp.nextLazy` goes through
`intProbeRows`, which reads the index. The index is rebuilt whenever the current `lazyIntHash` is not the map it was
built from, or has a different length:

- **Map identity.** The index holds the source map, so a freed map's address cannot be reused while the index still
  names it. Identity is compared through the map header pointer.
- **Map changes.** Every site that replaces, clears or shrinks the map changes one of the two values. Rows are
  appended to existing keys only during the build, before the first probe.

Maps over 2^20 keys keep the map lookup. Each participant of a parallel join builds its own index over a shared
table, and PG has one shared array for all of them, so a large copy per participant is not paid.

The `intProbeIndexDisabled` knob is used by the A/B and by the tests.

**Target.** TPC-H SF1 Q13, warm wall (`tools/tpch-gap.sh`, pass 2, 6 alternating rounds, median, host load < 2), with
instructions no worse than +1 %.

**Measured** (base `b5bd577ed`):

- **Wall.** 1.725 → 1.665 s median, **−3.5 %**; faster in each of the 6 rounds.
- **Instructions.** 11.419 / 11.414 → 11.393 / 11.392 G (−0.2 %).
- **Noise check.** The sf025 sweep total is flat against the census sweep (59531 vs 59533 ms). Its moving queries,
  Q74 and Q14, have flat instructions.

Gates:

- units;
- spotcheck;
- acceptance: 24 MATCH;
- sf025 PASS=99, with plans 99/99 same;
- TestPort\_RegressSuite PASS.

**Tests.**

- `TestIntProbeIndexMatchesMap` covers collision-prone keys (multiples of 2^40, negatives, the int64 extremes),
  absent keys, a same-length replacement map, a shrunk map and a nil map.
- `TestIntProbeIndexJoinsMatchMap` covers a multi-batch spill join, whose map is replaced per batch, and SQL inner,
  left, right, full, semi and anti joins on int, int8 and NUMERIC keys with NULLs.
- Dropping the freshness guard fails the first test; stopping at the first collision fails it too.

### Slice 2 (landed): the hashed aggregate's int-lane group table (C60)

`igroups map[int64]*groupRuntime` became `intGroupTable` (internal/executor/int\_group\_table.go):

- **Entries.** Interleaved `{key int64, g *groupRuntime}` entries, 16 bytes each, where a nil `g` marks an empty
  slot.
- **Growth.** It starts at 64 slots and doubles with a full rehash past a 2/3 load. PG's `build_hash_tables` sizes
  its table from the planner's group estimate instead; that is ledgered.
- **Lookup.** The same Fibonacci hash and linear probe as slice 1.
- **Lifetime.** Inserts and lookups interleave, so this is a real table owned by the aggregate's `Open`, not a
  frozen view.
- **Order unchanged.** Groups are still emitted in founding order (the `founded` / `iorder` lists), never in table
  order. The demotion to the string lane reads the table by `iorder`.

`aggIntTableDisabled` keeps the map, for the A/B and the tests.

**Sizing.** At `b0f3750d3` the int lane's map (`mapaccess1_fast64` + `mapassign_fast64` + `memhash64` under
`aggregateOp.Open`) was 10.8 % of H Q18's cycles and 6.3 % of H Q13's.

**Target.** TPC-H SF1 Q18, warm wall (6 alternating rounds, median, host load < 2), with instructions no worse than
+1 %.

**Measured** (base `b0f3750d3`):

- **Wall.** 3.875 → 3.75 s median, **−3.2 %**, meeting the bar. The rounds overlap: the host ran Q18 at 3.57–4.64 s
  that hour.
- **Load-insensitive check, 3 rounds.** User-mode cycles 13.87 → 12.92 G median (−6.9 %, lower in every round);
  instructions 32.35 → 31.72 G (−2.2 %).
- **Q13.** Wall flat (1.755 → 1.775 s median): its group lookups are a smaller share than the join probe slice 1
  took.

Gates:

- units;
- spotcheck;
- acceptance: 24 MATCH;
- sf025 PASS=99, with plans 99/99 same;
- TestPort\_RegressSuite PASS.

**Tests.**

- `TestIntGroupTableMatchesMap` covers 60 K keys through many doublings: multiples of 2^40, negatives and the int64
  extremes. Every key keeps its group, and absent keys find nothing.
- `TestAggregateIntGroupTableMatchesMap` runs SQL aggregates with tens of thousands of groups and a mid-stream
  NULL/fraction demotion. The output, order included, must equal the map's.
- Mutations: dropping the probe step loses groups, and an over-full table hangs.

### Later slices (open)

1. **Replace `lazyIntHash`.** Replace the map itself, removing the duplicate memory and the rebuild guard. That
   touches the batch, spill, shared-build and outer-fill sites, and the shared parallel build would then share one
   array as PG does. The right/full join's matched-flags map (`lazyMatchedI`, ~2.7 % of H Q13's cycles after slice
   1) goes with it.
2. **Size from the estimate.** Size the group table from the plan's group estimate, as PG's `build_hash_tables`
   does, instead of growing from 64.
