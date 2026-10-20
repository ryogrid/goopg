# M0146-0236 — `ctid` above a WindowAgg

Status: closed 2026-10-20 (the window case). Parent: M0146-0216. The defects found beside it are filed as M0146-0238
and M0146-0239.

## 1. Record

`SELECT ctid, row_number() OVER () FROM t` returned NULL for every row's ctid; PG returns each row's TID. Found by
M0146-0216 slice 5 while pinning COPY's heap layout, where a `lag(ctid)` back-jump query read nothing.

## 2. Mechanisms

- **PG.** `ctid` is a Var of its relation with attno -1 (SelfItemPointerAttributeNumber). The scan projects it like
  any column. make\_window\_input\_target puts the Vars a WindowAgg's targetlist needs into its input target, so the
  value travels up as a column.
- **goopg.** `ctid` resolves to `CTIDExpr`, which reads a TID side-channel on the slot (`hasCTID`, `ctidBlock`,
  `ctidOff`). The scan sets the channel. Single-child pass-through operators propagate it: project and filter copy it,
  and sort keeps it per row when a consumer asks through `markSortWantCTIDs`.
- **The gap.** `windowOp` buffers every input row (Materialize → Row), re-sorts them by partition and order keys,
  and emits fresh `MaterializedSlot`s, so the channel was lost. `markSortWantCTIDs` walked through the window to
  the sorts below without asking the window itself to keep anything.

## 3. Change

- `windowOp` gets `wantCTIDs` and a per-row `ctids` array. Open captures each input slot's `TID()` while buffering.
  The array follows the partition sort's permutation, and `emit` re-attaches the TID on output, as `sortOp` does.
- `markSortWantCTIDs` sets the flag on the windowOp it passes through.

## 4. Result and pin

- `ctid` beside window functions returns each row's TID. Checked with no ordering, with `ORDER BY id DESC`, with
  PARTITION BY plus ORDER BY, and through a run-condition subquery.
- On 200000 rows over a Sort, every TID is distinct and equals the row's own `ctid`.
- `TestCtidSurvivesWindowAgg` (postmaster) asserts three queries' (ctid, window value) sequences. Removing the flag
  turns them NULL.

## 5. Residuals (filed)

- **`ctid` inside the window's own expressions** (`PARTITION BY ctid`, `lag(ctid)`, a run condition) still reads
  NULL. windowOp evaluates them on bare buffered Rows. This is the same root as M0146-0238.
- **M0146-0238 (S2 wrong results).**
  - `a.ctid` / `b.ctid` in any join read NULL: the side-channel cannot carry two relations' TIDs.
  - Unqualified `ctid` binds to non-relation range entries. `SELECT ctid FROM (subquery) s` returns values where PG
    errors 42703, and `t, LATERAL (SELECT 1) l` reports a false ambiguity.
  - The PG-faithful fix makes system columns relation-bound columns that the scan projects.
- **M0146-0239.** `xmin`, `xmax`, `cmin` and `cmax` do not resolve at all (42703 where PG returns values).
