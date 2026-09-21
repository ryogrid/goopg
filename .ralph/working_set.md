(idle — nothing in flight)

# Loop #61 result — M-NIGHTLY AI-20260922-004850-016: 3 of 4 subtests FIXED

Banner scan: items 0-9 are all done or blocked (item 3 is the M0145-0001 `[!]`
escalation from loop #60; item 6's open subtasks are non-selectable by their
own gates; item 7 M0140-0007 awaits an owner decision a prior loop raised;
item 8's unfrozen M0142-0008 chain members each say "do not pick up ahead of
that unblock", and the sanctioned unblock landed with ZERO movement). So the
first selectable task was item 10's first in document order.

## Root cause — my own M0143-0007b storage flip, PRODUCER side
Slices 1-3 audited CONSUMERS. The gap was the other half: after storage
started padding, the CAST **to** `char(n)` was the only bpchar producer still
emitting a TRIMMED image, so one logical value had two representations.
- `lower`/`upper` consumed the padded image (PG has no `lower(bpchar)`; it
  resolves via the implicit rtrim1 cast) -> new `bpcharArgAsText` helper.
- cast to `char(n)` now pads (`varchar->bpchar` is `castfunc => '0'` in
  pg_cast.dat, so the typmod coercion `bpchar()` pads).
- `bpchar->varchar` now strips. My OWN earlier comment had excluded it
  claiming it is "a separate entry" — it is a separate pg_cast entry naming
  the SAME function (`text(bpchar)`). Separate-entry did not imply
  separate-behaviour; reading the catalog refuted it.

`union` was the dangerous one: UNION stopped DE-DUPLICATING and doubled every
row. The other two only mis-rendered a column WIDTH with correct values —
which is why every value gate stayed green.

## Test-design rule discovered (carry this)
`octet_length` is a `PadBpchar` RE-PADDING render caller, so it reports the
padded width whether or not the PRODUCER padded. My first cast-padding test
was written on it and PASSED with the fix disabled — verified, not suspected.
The pinned test now uses UNION de-duplication, a raw-image witness. Extends
slices 1-3's "re-padding sites are safe, consumers are dangerous" rule from
the code to the TESTS.

## Gates (all green)
units; FULL upstream regress suite (Rule #5) — only `partition_aggregate`
fails, unrelated; tpch-spotcheck PASS (Q12=2 Q13=33); tpcds-sf025 PASS
(`PLAN-SHAPE same=99 changed=0`); tpch-acceptance-arm 24/24 value-MATCH;
pgbench smoke via hook. New test verified non-vacuous; `initcap` annotated
honestly as a guard (passes either way — `initCap` already strips).
One pre-existing pin (`TestInlineCastVarcharBpcharTypmodTruncation`) had its
expectations RE-DERIVED from a live PG 18.3 reading, not flipped to match.

## Filed this loop
- `testport/TestPort_RegressSuite/partition_aggregate` — partitionwise
  aggregation, a planner feature, NOT the bpchar class.
- `bpchar-text-function-class` — every text function taking a bpchar strips.
  Measured on PG: `initcap`/`replace`/`substr`/concatenation all strip;
  goopg unverified for each. Includes the witness-design warning.

## Next loop
Item 10 continues: `partition_aggregate`, PgAmcheck003 x4 (-002..-005),
PgoutputInterop x10 (-006..-015), plus `bpchar-text-function-class`.

## Owner escalations OPEN — two, both M0145
1. M0145-0018's cost-model no-go.
2. M0145-0001 lineage budget exhausted (loop #60) — blocks ALL of item 3.
