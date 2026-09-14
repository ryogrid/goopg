# M0139-0006 — put the packed-retention decision to the owner

Status: accepted
Milestone: M0139 — Executor-side narrowing / projection pushdown
Type: decision packet (no production diff, no implementation decision made
here; per the plan-parity harness's recon-task carve-out in `AGENT.md`
§"Plan-parity harness" — "measurement plus a design note ... with no
production change")

## Why this doc exists, and what it is not

This is **not** an approval, a rejection, or a start of work on
`minimize_datum` (docs/design/not_ralph/minimize_datum/). It is the packet
M0139-S3 (`m0139-s3-k67-residue-measurement.md`) and M0139-0005
(`m0139-0005-q4-grouping-ratio-remeasurement.md`) both named as their
downstream consumer: `minimize_datum`'s README states, up front, **"Status:
NOT APPROVED TO START"**, and that status is unchanged by this doc. The
purpose here is to carry three pieces of evidence — one new (S3's measured
residue), one pre-existing but not yet stated next to S3's number
(`entrywidth.go`'s non-monotonicity finding), and one already on record
(the `minimize_datum` review's own two blockers) — into a single place so
whoever owns the `minimize_datum` go/no-go decision has them together
instead of scattered across three documents.

## 1. What M0139 itself measured: narrowing landed, but the residue is worse than budgeted

M0139-S1/S2 landed real executor-side join-leg narrowing (a join-leg hook
that drops unreferenced columns above the join, `internal/optimizer/joinleghook.go:69`).
M0139-S3 then measured, rather than assumed, what TPC-H Q12 narrows to at
HEAD:

- K67 (`r70-hash-footprint/SCOPE.md`) had **estimated**, before real
  narrowing existed, that the build side would land at **72 B/row → 103 MB**
  even "narrowed to one column" — an analytical figure,
  `hashsize.EntryBytes(1, 0) = 1·48 + 24 + 0`.
- S3 measured the real post-narrowing build side: **`{o_orderkey,
  o_orderpriority}` — 2 columns, not 1** (the join key cannot be dropped;
  it must stay in the entry for probe-time equality verification even
  though nothing above the join references it), with
  `avg_width(o_orderpriority) = 8.3701` real text bytes, not K67's assumed
  0. Formula and executor's own `EXPLAIN ANALYZE` agree: **≈128.4 B/row**,
  extrapolating to SF=1's 1.5M orders as **≈193 MB**.
- Against PG's own unchanged **22 B/row → 31 MB**, goopg's post-narrowing
  residue is **~5.8× PG's, not the ~3.3× K67 anchored the campaign
  against.**

**Consequence for this decision:** the case for *some* further lever beyond
column narrowing is, if anything, stronger post-measurement than it was
when `minimize_datum` was first scoped — the gap did not shrink toward "good
enough," it grew. That cuts in favor of considering `minimize_datum`. The
next two sections cut against acting on that alone.

## 2. `entrywidth.go`'s non-monotonicity finding: shrinking bytes/row does not monotonically shrink batch count

This finding predates M0139 (recorded under `minimize_datum/TODO_ALL.md`'s
"D-05 prereq #1" row, 2026-09-06) but has not previously been placed next to
S3's number. It matters here because it is a direct risk to the premise that
packing (`minimize_datum`'s mechanism) buys back what narrowing (M0139's
mechanism) leaves on the table:

> entry model fixed 194 → 120 B/row (was half-narrowed, half-full-width);
> **`NBatch` unchanged — D-04's claim refuted**; **non-monotone: 2 batches
> need ≤111.8 B/row; packed 63 B/row lands back on 4. The lever is
> `MapSlotBytes`, not the entry.**

In plain terms: cutting the per-row entry width from 194 to 120 bytes did
**not** reduce the hash join's batch count (still 4→4). The threshold to
drop to 2 batches sits at 111.8 B/row — close, but not reached by that cut —
and a *further* cut to a hypothetical 63 B/row (well past what 120 achieved)
**lands back on 4 batches**, not fewer. Batch count in this model is
governed by `MapSlotBytes` (the bucket-table term), not monotonically by
entry size. This is a structural risk specific to `minimize_datum`'s
justification: **packing entries smaller is not guaranteed to reduce
spilling**, and the geometry (`hashsize.Choose`'s `bucketSize`,
`RowSliceBytes`, `MapSlotBytes`, `estimatedRowBytes`) has to be re-derived
alongside any packing change, not assumed to fall out of it — which is
exactly what `minimize_datum`'s own MD-04 slice already proposes to do
(see §3).

## 3. `minimize_datum`'s own review already raised two blockers that this evidence does not remove

The bundle's adversarial review (2026-09-03, recorded in
`minimize_datum/REVIEW.md`, restated in `minimize_datum/README.md`) found
three blockers; the licence claim (B1) was withdrawn outright, but two
remain load-bearing and are unaffected by S3's new number:

- **B2/B3 — the premise was modelled, not measured, and the measured
  answer is smaller than the model.** The bundle's original justification
  (`FINDING-p401-alone-is-not-enough.md`) fed hand-derived column counts
  into `hashsize.Choose` — no query ran. The executed measurement
  (`.ralph/deferral_ledger.md:2036`, Q9, equal cardinality) put goopg's
  widths at 1098/1642/2090/3164 B vs PG's 23/32/54/81 B. Arithmetic from
  that pair: **packing alone closes ~5× of a ~48× gap; narrowing (M0139's
  own mechanism) is the larger term and owns the rest.** S3's Q12 number is
  a second data point in the same direction, not a counter-example: even
  after M0139's narrowing landed, the *residual* gap (128.4 vs PG's 22 B/row,
  ~5.8×) is on the same order as what packing alone was independently
  estimated to close (~5×) — i.e., S3 does not show packing would close the
  post-narrowing gap; it shows the post-narrowing gap is roughly
  the size packing was already estimated to address, which is a much weaker
  claim than "packing is the fix."
- **B5 — sequencing contradicts take3 13 §8.2.** `EX1 before EX3's
  geometry`: batch counts, arena sizing and spill thresholds must be
  computed over *narrowed* widths, and `minimize_datum`'s MD-04
  (which re-derives `EntryBytes`, `bucketSize`, `RowSliceBytes`,
  `MapSlotBytes`, `estimatedRowBytes` — the batching geometry) is
  "premature by rule" ahead of that. **M0139-S1/S2 is the concrete
  EX1-equivalent this sequencing rule was waiting on, and it has now
  landed and been measured (S3).** This is the one respect in which the
  situation has materially changed since the review: the sequencing
  *prerequisite* for MD-04 is now satisfied. It does not by itself imply
  MD-04 should proceed — only that the specific "premature by rule" blocker
  no longer applies verbatim. §2's non-monotonicity finding is the reason a
  green light on sequencing is not the same as a green light on
  effectiveness: MD-04 would still have to re-derive the geometry from
  scratch, and that re-derivation's own history (this same finding) shows
  it does not trivially reward shrinking the entry.

## 4. The question for the owner

Given all three points together, the packet is:

> `minimize_datum`'s sequencing prerequisite (narrowing landing and being
> measured, i.e. M0139-S1/S2/S3) is now satisfied. The post-narrowing
> residue it would need to close is real and non-trivial (128.4 B/row
> measured, not 72 B/row assumed — ~5.8× PG). But the bundle's own review
> already established that packing alone was expected to close only ~5× of
> a much larger (~48×) gap, with narrowing (already done) owning the rest —
> and a structural finding independent of this milestone
> (`entrywidth.go`'s non-monotonicity) shows that shrinking entry bytes does
> not reliably shrink batch count without also re-deriving the bucket-table
> geometry, which is exactly the work MD-04 would have to redo. Separately,
> `minimize_datum`'s stated goal is byte-parity / a single retention format,
> not closing a plan-*structure* parity gap — and plan-structure parity
> (not memory footprint) is this milestone group's (M0137–M0143) own success
> metric. **Decision needed:** authorize `minimize_datum` (MD-01..MD-04) to
> start now that its take3 §8.2 sequencing blocker is resolved, or hold it
> as out-of-scope for the M0137–M0143 campaign (whose currency is plan
> structure, not footprint) and revisit separately?

No implementation follows from this doc either way. `minimize_datum` remains
**NOT APPROVED TO START** until the owner answers.

## Gates

Decision-packet task: no production diff (`git diff --stat` against the
production tree is empty for this task). No new test — this task
synthesizes existing measurements (S3, the `entrywidth.go` TODO_ALL row, the
`minimize_datum` REVIEW.md blockers) rather than producing a new one. No
ledger row: no new PG-incompatibility surfaced here; the doc is the record
itself, not a forward reference.
