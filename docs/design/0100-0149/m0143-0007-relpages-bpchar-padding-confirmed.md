# M0143-0007 — direct per-page measurement confirms bpchar blank-padding (R23) as K41's relpages cause

*Status: recon complete (no production code changed). Confirms and quantifies
the K41 heap-density gap on `customer`/`item`; implementing the actual fix
(R23) is filed separately as M0143-0007b, pending an owner decision on the
tradeoff described in §4.*

## 1. Background

K41 (`r4-heap-density/FINDINGS.md`, `.ralph/deferral_ledger.md` row
`m0140-0005-nonplanner-heap-density-floor`, 2026-09-15): goopg's TPC-DS
`customer`/`item` heaps store the same row count as PG in **fewer** pages —
`customer` 1,979 (goopg) vs 2,872 (PG), `item` 716 vs 1,284. `relpages` is a
direct input to every page-priced cost term, the Mackert-Lohman index-cost
estimate, and `compute_parallel_worker`'s size ladder, so this is a planner
input error no cost-model change can correct.

Two competing mechanisms were on record as candidates, both still marked `[ ]`
in `docs/design/not_ralph/plan_parity_fix_take2/TODO.md:915-921`:

- **R22 — heap page fill on bulk load.** goopg's pages could simply be less
  full than PG's (more free space per page → fewer rows needed to explain
  the same page count, i.e. a *fill* difference, not a *tuple width*
  difference).
- **R23 — `character(N)` blank-padding.** PG's `bpchar` storage pads every
  value out to its declared width; goopg stores it trimmed
  (`internal/catalog/bpchar.go`). If confirmed, this shrinks every tuple that
  carries a `char(N)` column, which both `customer` and `item` do heavily.

R5 (2026-09-08, older measurement) had already called R23 "confirmed" as a
*hypothesis consistent with the data*, but explicitly by inference from page
totals, and both the ledger row and `TODO.md:915-918` require redoing this as
a **direct per-page free-space comparison** — "do NOT infer from totals
again" — because the page counts had since drifted (K39's bulk-load fill fix
changed goopg's numbers between R5 and now) and because inferring from totals
cannot separate "tuples are narrower" from "pages are less full".

## 2. Method

Both engines write the identical PG18 page format: `SizeOfPageHeaderData =
24`, `pd_lower`/`pd_upper` as little-endian `uint16` at byte offsets 12/14
(`internal/storage/page.go:18,156-160`, matches
`postgres/src/include/storage/bufpage.h`). This means per-page free space
(`pd_upper - pd_lower`) can be read directly off the raw relation files on
disk — **without starting either server** — for both engines, using the same
8 KiB page-header offsets.

Relation files were identified by exact byte-size match against the known
`relpages` counts (`relpages * 8192`), confirmed against PG's own
`pg_class.relpages`/`reltuples` for db `tpcds025` (SF=0.25, matching the
already-running `:65438` reference cluster — read-only, no DDL/DML run
against it):

| table | PG relfilenode | goopg relfilenode |
|---|---|---|
| `customer` | `bench/tpcds/runtime/pgdata/base/16586/16671` | `bench/tpcds/runtime_goopg/data-sf025/base/5/16449` |
| `item` | `bench/tpcds/runtime/pgdata/base/16586/16644` | `bench/tpcds/runtime_goopg/data-sf025/base/5/16437` |

A page reader parsed `pd_lower`/`pd_upper` from every 8 KiB page of each file
and summed `used = 8192 - 24 - free` across all initialized pages.

## 3. Result

| table | engine | pages | avg free/page | total used bytes | reltuples | avg used B/row |
|---|---|---|---|---|---|---|
| `customer` | PG | 2872 (2854 init) | 117.3 B | 22,976,664 | 100,000 | 229.8 |
| `customer` | goopg | 1979 (1979 init) | 82.8 B | 16,000,648 | 100,000 | 160.0 |
| `item` | PG | 1284 (1238 init) | 255.5 B | 9,795,616 | 18,000 | 544.2 |
| `item` | goopg | 716 (716 init) | 173.4 B | 5,724,112 | 18,000 | 318.0 |

Page-count ratio vs used-bytes ratio, goopg/PG:

| table | page ratio | used-bytes ratio |
|---|---|---|
| `customer` | 0.689 | 0.696 |
| `item` | 0.558 | 0.584 |

**The two ratios are nearly identical in both cases.** That is the
discriminator this task needed: if R22 (free-space/fill) were doing
meaningful work, the used-bytes ratio would sit measurably above the
page-count ratio (PG "wasting" more space per page, not per row). Instead
goopg's average free space per page is *smaller* than PG's on both tables
(82.8 vs 117.3 B; 173.4 vs 255.5 B) — goopg's pages are packed **more**
tightly than PG's, the opposite of what an R22-style fill deficit would
require. The entire page-count gap collapses to **used bytes per row**,
i.e. tuple width, which is exactly R23's mechanism.

Cross-checked against a second, independent method: summing `(declared_width
- trimmed_length)` directly over the SF0.25 load TSVs for every `bpchar`
column gives 67.9 B/row (`customer`) and 229.3 B/row (`item`) — matching the
per-page-derived deltas of 69.8 and 226.2 B/row to within a few percent,
itself inside the residual noise from tuple header/alignment overhead. Both
methods agree.

## 4. Conclusion and disposition

**R23 (`character(N)` blank-padding) is confirmed as the ~full explanation**
for K41 on `customer`/`item`; **R22 (heap page fill) is ruled out** as a
contributing cause in this direction for these two tables (goopg's pages are
denser, not sparser). This closes the "separate the two" ask of M0143-0007.

Implementing R23 itself — storing `bpchar` padded on disk to match PG — is
**not** done in this task, and is filed separately as **M0143-0007b**,
because it is not a narrow fix: `internal/catalog/bpchar.go`'s trimmed
storage is explicit, documented, load-bearing design (M0103-0007 rung 24),
relied on by `compareDatum`'s padding-insensitive bpchar equality and by
`internal/executor/codec.go`'s `coerceTextLikeDatum`, with the render-time
`PadBpchar` boundary intentionally restoring padding only at output
(`internal/executor/bpchar_declared_width_test.go:78-86` states explicitly
why the reverse — padding on decode — would be wrong). Reversing it touches
every sibling boundary that currently assumes trimmed storage (heap
comparisons, `internal/access/nbtree` key comparators, WAL `pgoutput`
encoding) and changes on-disk tuple width store-wide (TOAST thresholds,
index key sizes, WAL record sizes) — a multi-slice project needing its own
design and owner sign-off on whether to reverse the trimmed-storage
convention at all, not a same-loop follow-up.

## 5. Artifacts

- Page-reader script: ad hoc, not committed (pure stdlib `struct` parse of
  `pd_lower`/`pd_upper`; trivial to reproduce from §2's method table).
- Ledger: `.ralph/deferral_ledger.md`, row dated 2026-09-18 (task
  M0143-0007).
