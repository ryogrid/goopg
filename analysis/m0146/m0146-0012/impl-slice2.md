# M0146-0012 impl slice 2: delete flattenStrandedSeqScanFilters (2026-10-05)

Code: `1db4edacc`.

## Question

The cutover restored two producers for correlated one-relation bodies (Q17,
Q20), and slice 2 asked whether each still earns its place:

- the rule (`planIndexScanFromWhere`, M0145-0008);
- its multi-conjunct twin, `flattenStrandedSeqScanFilters` (M0145-0027).

## Method

Temporary knobs (removed before commit) skipped either the rule for a scope
whose Filters read an outer level, or the flatten everywhere. Each variant
was then checked four ways:

- TPC-H: all 22 plans via `tpch-acceptance-arm.sh … -explain`, on a binary
  rebuilt with the knobs;
- TPC-DS SF0.25: plans via `tpcds-sf025-regression.sh plans`;
- optimizer and executor unit suites;
- regress `join`, A/B against a HEAD worktree, two runs each.

## Findings

- **TPC-H:** all 22 plans are identical with both knobs on. Neither
  producer fires on TPC-H any more: the search builds Q17's and Q20's
  probes itself (M0146-0015a). The arm is still 24/24 MATCH.
- **TPC-DS SF0.25:** only Q6 and Q41 change, and only in cost; every
  shape and filter order is the same. Both changes come from the flatten.
  Skipping the rule as well changes nothing more.

  | node | before | after | PG 18.3 |
  |---|---|---|---|
  | Q41 SubPlan Aggregate / correlated `item` scan | 180 | 3987 | 4029 |
  | Q41 outer `item i1` Seq Scan | 180.83 | 1512.83 | (includes SubPlan × rows) |
  | Q6 decorrelated `item` HashAggregate | 243 | 1512 | n/a (PG keeps a SubPlan) |

  PG figures were EXPLAINed on `:65438` db `tpcds025`, read-only
  (`impl-slice2/q41-pg-explain.sql`). The flatten replaced a searched leaf
  with an unsearched `Filter{SeqScan}` priced well under PG.

  Sweep timings are checksum-identical: Q41 went 18.2 s (two pre-slice
  sweeps) → 10.3 s / 11.9 s (two after); Q6 stayed at about 0.4 s.
  Plans: `impl-slice2/tpcds-sf025-Q{41,6}-{before,after}.plan.txt`.
- **Regress `join` (rule skipped for correlated scopes):** one real hunk.
  The query is `lateral (select f1 from int4_tbl where f1 = any (select
  unique1 from tenk1 where unique2 = v.x offset 0))` over
  `(values (0,9998), (1,1000)) v(id,x)`. Its `tenk1` probe fell from Bitmap
  Heap Scan to Seq Scan; PG has an Index Scan on `tenk1_unique2`.

  Cause: the outer key `v.x` is `int8` in goopg, because goopg types every
  integer literal `bigint` (PG: `int4`). `restrictionKeyUsable` refuses an
  uncast `int8` key against an `int4` index column, while the rule builds
  the probe anyway. Probing on a real server:

  | | goopg | PG 18.3 |
  |---|---|---|
  | `select pg_typeof(9998)` | `bigint` | `integer` |
  | `select 2147483647 + 1` | `2147483648` | `ERROR: integer out of range` |

  Filed as **M0146-0062** (S2, escalated).
- **Unit pins** `TestOneRelIndexProducerKeeps*` (optimizer, InMemory
  catalog): without the rule or the flatten, the body stays a Seq Scan and
  is decorrelated. That catalog has no storage, so the search prices the
  Seq Scan under the probe. Through the real catalog, never-analyzed tables
  give PG's plans whether or not the rule exists
  (`TestCorrelatedOneRelBodyProbesThroughSearch`).

## Decision

- Delete the flatten. It is plan-neutral on TPC-H, cost-only and toward PG
  on TPC-DS, and makes Q41 faster.
- Keep the rule, correlated half included, until M0146-0062 lands. It still
  builds the regress `join` probe that the int8-typed literal key denies
  the search.
- `TestFlattenStrandedSeqScanFilters` goes with the flatten. The
  multi-conjunct (Q20) unit pin measured the flatten on the storage-less
  catalog, so it becomes the executor test above. The single-conjunct
  (Q17) pin still passes through the rule.

## Gates (final code)

- Unit suite PASS; tpch-spotcheck PASS.
- TPC-H arm 24/24 MATCH.
- SF0.25 sweep PASS.
- Fire set PASS. Categories are unchanged at both scales: SF0.25
  join-order 49, aggregation-strategy 9; SF1 join-order 55,
  aggregation-strategy 17.
- ea-ratchet PASS (9/9).
- Regress A/B over the 14-file set: only the `join` row-order flap (it
  flaps between two HEAD runs too), the known plpgsql flap and rowsecurity
  pointer addresses.

## Still open in M0146-0012

- The rule's correlated half: retire it after M0146-0062.
- Multi-relation EXISTS bodies (slice 1 residual).
