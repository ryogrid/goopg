# M0145-0008w recon: why pgbench's small tables grow — pd_prune_xid keeps the newest xid

Status: **RECON COMPLETE 2026-09-25**. No production code changed; the probes
were uncommitted builds. Task: `.ralph/fix_plan.md` M0145-0008w (Kind: recon,
Parent: M0145-0008v). Evidence: `analysis/m0145/m0145-0008w/measurements.txt`.
Filed: **M0145-0008x**.

## Question

After the line-pointer lifecycle (M0145-0008v), `pgbench_branches` (2 rows)
still reached ~85 heap blocks in a 30 s TPC-B run, against 7 on PG 18.3.
A page dump showed emptied pages carrying free lines that nothing inserted
into again.

## Measurements

The workload is pgbench TPC-B at scale 2, 8 clients, 30 s.

- **goopg, HOT counters** (`GOOPG_S321_PROBE=1`): about 88% of updates were
  HOT. `hot_nospace` was about 5,100: the page was full, so those updates
  went non-HOT and moved their row version off the page.
- **PG 18.3, same workload:** branches 99.99% HOT (156,408 of 156,426),
  tellers likewise; branches 7 blocks, tellers 2 blocks.
- **goopg, on-access prune counters** (uncommitted probe build):
  - 22,000 page reads, of which 18,575 carried a `pd_prune_xid` hint;
  - only **258** passed the gate, and all 258 pruned;
  - the conditional cleanup lock was **never** refused, and the HOT path
    lacked the only pin just 36 times.
  - So the cleanup-lock discipline (M0145-0008q) is not the obstacle; the
    gate is.

## Cause

PG keeps the **oldest** prunable xid in `pd_prune_xid`. `PageSetPrunable`
(`postgres/src/include/storage/bufpage.h`) replaces it only when the new
xid precedes it.

Five of goopg's six setters in `internal/storage/heap.go` do the opposite,
`if xmax > pruneXID { SetPruneXID(xmax) }`, and so keep the **newest**:
- the xmax stamp for delete;
- the xmax stamp for a non-HOT update;
- the HOT old-tuple stamp;
- the chain-update stamp;
- the multixact updater stamp.

Only the sixth, the one near line 1847, follows PG. On a page that is
updated continuously, the hint always equals the latest updater's xid,
which is never older than the horizon. Both prune entry points then reject
the page:
- the on-access gate (`PagePruneOnAccessWanted`);
- the HOT path's `PagePruneOpt`, in its `pruneXID >= oldestXmin` fast path.

The page fills with dead HOT versions, the next update finds no space and
goes non-HOT, and the table grows.

## Confirmation

An uncommitted build with PG's rule at the five setters, same pgbench run:

| | HEAD | keep-oldest probe | PG 18.3 |
|---|---|---|---|
| `pgbench_branches` blocks | 85 | **18** | 7 |
| `pgbench_tellers` blocks | 91 | **0** | 2 |
| `hot_nospace` | 5,211 | 3,547 | — |
| tps | 641 | 662 | — |

The remaining no-space cases are mostly `pgbench_accounts`, whose full
pages give PG its ~4% non-HOT updates as well.

## Filed

**M0145-0008x** (impl): port `PageSetPrunable` to the five setters. Check
the redo arms and the `XIDPrecedes` wraparound order; pin with a unit test
and the pgbench block counts.

Movement: none (recon).
