# M0146-0209 — TPC-DS SF1 Q18 (sort-strategy): findings (2026-10-16)

The question was why goopg plans a Gather Merge over per-worker Sorts below Q18's grouping-sets GroupAggregate, where
PG plans a Sort over a Gather.

## What PG does (a PG 18.3 `-DOPTIMIZER_DEBUG` build with `elog` probes)

- **Setup.** The scratch copy `tmp/pgdbg/src` was patched with temporary probes, which are now removed. The six Q18
  tables were dumped from `:65438` (db `tpcds`) and analysed. Its plan has the same shape as the reference
  (`q18-pgdbg.plan.txt`).
- **The Gather Merge is built.** At the final scan/join rel, `generate_useful_gather_paths` builds it at
  45978.62..45984.0087 over 45 rows (`pgdbg-gm-and-gset-inputs.txt`, last GM line). That is cheaper than
  Sort-over-Gather (45984.11..45984.22).
- **It never reaches the grouping step.** The inputs `consider_groupingsets_paths` sees are the Sort over the Gather
  (tag 303), a sorted serial path (tag 301) at 45962.60..46431.42, two other GMs, and the Gather.
- **Why it is rejected.** `add_path` (pathnode.c) finds the new GM fuzzily equal to the serial sorted path.
  - Startup ratio: 1.00035; total ratio: 46431.42 / 45984.01 = 1.00973, inside STD\_FUZZ\_FACTOR 1.01.
  - The pathkeys and rows are the same, so the tie-break checks parallel safety first. A Gather Merge is not
    parallel-safe and the incumbent is, so the GM is rejected.
  - The Sort over the cheapest Gather therefore wins.

## What goopg does

- **The same candidates.** goopg builds the same set: Gather 46014.33, Gather Merge 46009.70..46015.57, and a serial
  sorted nested loop at 45969.38..46546.69 (`goopg-dppath-top-head.txt`). Its `addPath` has the same
  parallel-safety tie-break (path.go:1205).
- **Outside the fuzz at HEAD.** The serial path to GM ratio is 46546.69 / 46015.57 = 1.01154, just outside the fuzz, so
  both paths survive and the Gather Merge wins at the grouping step.
- **Part of the gap is the corpus.** The goopg SF1 corpus has relallvisible 0 on every table (never vacuumed); PG's is
  about 99% all-visible. The serial path's `customer_demographics cd2` index-only probe therefore pays heap fetches:
  2.67 per probe against PG's 0.88. That is the M0146-0019 owner residual.
- **After VACUUM on a private clone.** The six tables now have relallvisible = relpages. The GM falls to 45992.51 and the
  serial path to 46455.41, a ratio of 1.01007. That is still outside 1.01, by about 3 cost units (0.007%), so the plan
  is unchanged (`q18-goopg-after-vacuum.plan.txt`).
- **The rest is estimate noise.** The remaining spread is in the row estimates, which vary with the ANALYZE sample:
  goopg 49 rows, reference PG 53, debug PG (a fresh ANALYZE of the same data) 45.

## Disposition

This is a COSTTIE residual. Two of PG's own paths sit 0.97% apart and are separated by add\_path's 1% fuzz plus its
parallel-safety tie-break. goopg's equivalent pair sits 1.15% apart, and still 1.007% after the corpus VACUUM. goopg's
add\_path logic is faithful, and no costing mechanism departs from PG's. What departs is the input estimates (the
ANALYZE sample and the unvacuumed corpus), and a 3-unit margin cannot be moved by porting.
