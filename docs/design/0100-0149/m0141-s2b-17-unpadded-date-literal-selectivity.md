# M0141-S2b-17 — the two divergences left after S2b-15's repin triage

Status: complete 2026-09-24; the fix landed as M0141-S2b-17b. Task: `.ralph/fix_plan.md` M0141-S2b-17 (Kind:
recon, Parent: M0141-S2b-15).

## (a) split-aggregate `rows=1` — already resolved

S2b-15 recorded Finalize / Gather / Partial HashAggregate rendering `rows=1`
on TPC-DS Q62/Q99, where PG shows the group estimate. On the current tree
(SF0.25 plan capture) all three nodes show **120** on Q62 and **72** on Q99,
PG's figures. An intervening commit fixed it; nothing to do.

## (b) Q94's semi-join estimate — a date literal the planner could not parse

S2b-15 saw Q94's `Hash Semi Join` estimated at 90 rows against PG's 1
(actual 4). On the current tree the divergence sat one level lower. The
`web_sales ⋈ date_dim` probe estimated **2 020** rows, where PG estimates
**10**, i.e. PG's selectivity is the 60-day range read off the histogram
(10/12 494 ≈ 60/73 049).

Q94's filter is `d_date between '2002-5-01' and (cast('2002-5-01' as date)
+ 60 days)`. PG's EXPLAIN shows the upper bound folded to
`'2002-06-30 00:00:00'::timestamp`. goopg printed it unfolded. goopg does
fold `date + interval` (`tryFoldTemporalBinaryOp`, foldconst.go), but its
literal parser (`parseTemporalLiteral`) and the histogram-side parser
(`numericValue`, selectivity.go) accepted only zero-padded Go layouts
(`2006-01-02`). PG's `date_in` accepts `2002-5-01`, and so does the
executor's own parser (`nodes.parseDateFields`). So the fold declined, the
lower bound's histogram lookup fell to a flat fraction, and the range got a
default selectivity.

**Fix.** `padISODateLiteral` (selectivity.go) zero-pads the month and day of
a 4-digit-year dash date, keeping any time suffix. Every other spelling is
returned unchanged. It is applied in `parseTemporalLiteral` and in
`numericValue`'s `date` and `timestamp` arms. The fold's value agrees with
the executor's `addTimeInterval`, as `tryFoldTemporalBinaryOp`'s own
contract already required.

Pinned by `TestPadISODateLiteral`, `TestFoldUnpaddedDatePlusInterval` (fails
without the fix) and `TestNumericValueUnpaddedDate`.

## Result (TPC-DS SF0.25, values unchanged, no runtime move)

- **Q94**: probe 2 020 → 10, Gather 113 → 1, Hash Semi Join 90 → 1, Hash
  Anti Join 25 → 1. These are PG's estimates node for node.
- **Q95** (same date pattern): estimates move the same way.
- **Q16**: the `catalog_sales ⋈ date_dim` join now elects an index-probe
  Nested Loop. PG elects a `Parallel Hash Join` over a partial `date_dim`
  there, which goopg cannot build (partial-inner hash build: M0146-0002).
  Q16's plan differed from PG's before and after.
- `make ea-ratchet`: Q94 and Q71 FIXED; **2 NEW** (Q16, Q95), both Hash
  Semi Joins whose relation sets PG's plans do not contain (`pg_est=null`),
  so they are scored against the absolute bar (qerr > 10).
  - Q95: goopg est 1, actual 22. PG's equivalent semi join also estimates 1.
  - Q16: goopg est 4, actual 145. PG's equivalent pre-semi scope estimates
    about 2 per worker (about 5 total).
  - Before this fix the date range was over-estimated, which happened to
    land these nodes closer to their actuals. The new estimates are
    PG-faithful. Per AGENT.md's gate table (ledger row plus owning task per
    NEW finding class), they are ledgered with an owning recon task. No
    repin (G4).

## Not done (ledgered)

`date_in` accepts many more spellings (`May 1 2002`, `2002/5/1`, DateStyle
orders, 2-digit years, BC). The planner parsers still take only the ISO dash
form, so those literals still get no histogram estimate or fold.
