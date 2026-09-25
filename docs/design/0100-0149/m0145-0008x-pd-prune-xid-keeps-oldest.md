# M0145-0008x: pd_prune_xid keeps the oldest prunable xid, as PG does

Status: **LANDED 2026-09-25**. Task: `.ralph/fix_plan.md` M0145-0008x (Kind:
impl, Parent: M0145-0008w). Recon: `m0145-0008w-pd-prune-xid-keeps-newest-recon.md`.
Evidence: `analysis/m0145/m0145-0008w/pgbench-0008x.txt`.

## Change

PG's `PageSetPrunable` (`postgres/src/include/storage/bufpage.h`) only ever
**lowers** `pd_prune_xid`: the page keeps the oldest xid that might have
made a tuple prunable.

goopg already had that rule as `storage.pageSetPrunablePG`, but only its
redo helpers used it. The five producers **raised** the hint to the newest
xmax instead:
- `PageSetHeapTupleXmax`
- `PageSetHeapTupleMovedPartition`
- `PageStampHotOldTuple`
- `PageStampUpdatedOldTuple`
- `PageStampHotOldTupleMulti`, with the updater xid

They now all call the helper, and the helper compares with `XIDPrecedes`,
which is wraparound-safe like `TransactionIdPrecedes`.
`PagePruneOpt`'s fast path, which used a plain `>=`, uses the same modular
order. Runtime and redo pages now agree on `pd_prune_xid`; before, the redo
helpers kept the oldest and the producers the newest.

Why it mattered (the recon): on a continuously updated page, the newest-xid
hint never preceded the horizon. The on-access prune and the HOT path's
`PagePruneOpt` both skipped the page, it filled with dead HOT versions, and
updates went non-HOT.

## Measured

pgbench TPC-B, scale 2, 8 clients, 30 s, fresh clusters, alternating arms:

| | HEAD | candidate | PG 18.3 |
|---|---|---|---|
| `pgbench_branches` blocks | 73, 85 | **21, 13** | 7 |
| `pgbench_tellers` blocks | 80, 94 | **1, 1** | 2 |
| `pgbench_accounts` blocks | 3335 | 3335–3336 | 3384 |

tps was dominated by host noise on both arms: 551 / 647 on HEAD against
671 / 518 on the candidate.

## Tests

- `TestProducersKeepOldestPruneXID`:
  - a newer deleter does not raise the hint, and an older one lowers it;
  - the hint precedes a horizon past the oldest deleter;
  - across the xid wrap point, a wrapped (newer) xid does not replace an
    older pre-wrap one.
- `TestPageSetHeapTupleXmaxUpdatesPruneXID` (storage) and
  `TestPageSetXmaxTracksPruneXID` (executor) had pinned the keep-newest rule.
  They now pin PG's rule.

## Gates

- units, `tpch-spotcheck`, acceptance (24 MATCH) and `tpcds-sf025` (96/96,
  99/99 shapes, two speed-ups) pass.
- Isolation family and full regress: only the failures HEAD also has.

Movement: none. No plan or value moved; the table-growth numbers above are
outside the parity instruments.
