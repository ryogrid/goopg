(idle — nothing in flight)

# Loop #62 result — partition_aggregate SIZED and ESCALATED `[!]`

Banner: items 0-9 unchanged from loop #61 (all done or blocked; item 3 is
M0145-0001's `[!]`). First open item-10 task in document order was
`partition_aggregate`, whose own note said "size this before starting".
No production code changed. Recon + escalation only.

## What the recon established (measured, not read)
1. **Not a regression; it never passed.** Built and ran the case in an
   isolated worktree at `aef4a4257` — before M0143-0007b slice 1, i.e.
   before any bpchar work — and it fails there with the byte-identical
   message. The nightly's "new tonight" label is NOT a code change; I could
   not explain the label and did not guess.
2. **It needs TWO unimplemented features.** The upstream file enables BOTH
   `enable_partitionwise_aggregate` and `enable_partitionwise_join`, then
   runs 115 queries. goopg declares both GUCs (catalog.go:12302-12306) with
   PG's own `off` default and consumes NEITHER.
3. **The optimizer is not partition-aware at all.** `catalog.Table` carries
   `PartitionKey`/`PartitionMethod`/`PartitionBounds`
   (catalog.go:649-660) but every reader is in `internal/executor`;
   `internal/optimizer` never reads them. goopg's plan is the CORRECT plan
   for `enable_partitionwise_aggregate = off` — self-consistent, just
   missing the feature.

## The escalation (governance, not engineering)
The inventory CSV line 139 marks this case `status=pass,
pass_required=yes` — "currently passing, must stay passing" — while the
SAME row's rationale says "output diverges". The measurement shows the
rationale is the accurate half. The must-pass set is exactly the
`status=pass` rows (`regressMustPass`, regress_suite_test.go:182), so this
one row keeps a pass-required gate permanently red on an unbuilt feature.
**I did NOT demote it.** Demoting is the obvious way to green the gate,
which is exactly why it is the owner's call; the documented workflow only
covers promotion. Options recorded: (i) correct to `status=failed`, or
(ii) keep must-pass and schedule the feature.
Verified single-row, not systematic: 21 rows carry the same stale
"diverges" rationale but the suite is green on all of them except this one.

## Deliberately NOT bundled
goopg's EXPLAIN renders `< 15` where PG renders `< '15'::numeric`. Real
divergence, much smaller — but fixing it alone would NOT make the case
pass, since the plan SHAPE still differs. Recorded so it is neither lost
nor mistaken for the whole gap.

## Gates
`go build ./...` OK; state guard OK; pgbench smoke via the commit hook. No
value gates required — zero production diff. Worktree removed.

## Next loop
Item 10 continues, next in document order: **`bpchar-text-function-class`**
(bounded; PG measurements already recorded, and it carries a
witness-design warning: do NOT measure through `octet_length`, it re-pads).
Then PgAmcheck003 x4 (-002..-005), PgoutputInterop x10 (-006..-015).

## Owner escalations OPEN — now THREE
1. M0145-0018's cost-model no-go.
2. M0145-0001 lineage budget exhausted (loop #60) — blocks ALL of item 3.
3. **NEW:** partition_aggregate's inventory row (this loop).
