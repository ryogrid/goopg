# M0146-0040: date ± integer and date − date follow PG's date operators

Status: landed 2026-10-02 (banner item 2a, S2).

## Defect

PG 18.3's date operators (all `provolatile = 'i'`):

| operator | function | result |
|---|---|---|
| `date + int4` | `date_pli` | date |
| `int4 + date` | `integer_pl_date` | date |
| `date - int4` | `date_mii` | date |
| `date - date` | `date_mi` | int4 days |

goopg failed in five ways across four layers:

- **Executor literal.** A date literal (`TypedStringLit` "date") evaluated
  to a plain timestamp datum, both on first use and from its cache. So
  `'2001-07-15'::date + 30` missed the executor's date arm and raised
  "operator + requires integer operands".
- **Analyzer.** It typed every cast `unknown` and `current_date` `unknown`,
  and had no `date - int` or `date - date` rule. So `d - 30` raised
  "requires numeric operands", `d <= '…'::date + 30` failed as "date and
  int8", and `n + d` failed for an `int` column (the type name `int` was
  missing from `isIntegerLike`).
- **Planner `exprType`.** No date rules, so `d + 30` was advertised as
  `unknown` and printed as a timestamp (`2001-08-14 00:00:00.000000`).
- **date − date.** It returned an interval (`14 days`), recorded as a
  "deliberate divergence" from when dates were plain timestamps.
- **No fold.** PG folds `'2001-07-15'::date + 30` to `'2001-08-14'::date`;
  goopg left the expression unfolded.

## Change

- **Executor** (`evalTypedStringLit`): a date literal yields
  `NewDateDatum`. The cache-hit path restores the date and timestamptz
  subtypes.
  - `subDateDate` is `date_mi`: an integer day count, with 22008 "cannot
    subtract infinite dates".
- **Analyzer**:
  - a cast to `date` types as date, and other casts keep `unknown`;
  - `current_date` types as date;
  - `date ± integer` → date, and `date - date` → int4;
  - `isIntegerLike` accepts `int`.
- **Planner `exprType`**: `date ± integer` / `integer + date` → date,
  `date - date` → int4, `date ± interval` → timestamp. These are the
  analyzer arms' twins.
- **Constant folding** (`tryFoldDateIntegerOp`, beside the existing
  date ± interval fold):
  - date literal ± integer → date literal;
  - integer + date literal → date literal;
  - date literal − date literal → integer;
  - infinite, BC or unparseable dates decline.

## Verification

`TestDateIntegerArithmeticMatchesPG` checks 15 values and types, plus the
folded EXPLAIN `Filter: (d <= '2001-08-14'::date)`, all against PG 18.3.
Before the change the query fails at analysis.
`TestTimestampSubtractionInterval`'s date − date case moves from the old
`9 days` to PG's `9`.

- Regress A/B: `date` shrinks 725 → 653 diff lines (72 fixed, none
  introduced). `horology`, `timestamp`, `timestamptz`, `interval` and
  `expressions` are unchanged.
- Gates pass: units, spotcheck, sweep 96/96, arm (values identical), fire
  set (fired none) and ea-ratchet.

## Left open (ledgered)

- Integer literals type as `bigint` in goopg (`pg_typeof(30)`; PG says
  `integer`), so `date + int8` is still accepted where PG errors with
  "operator does not exist: date + bigint".
- Casts to types other than date still analyze as `unknown`.
- `IntervalLit` has no `exprType` arm, so `date ± interval '…'` is
  advertised as `unknown` and prints with microseconds.
- An error raised while folding a literal keeps a cursor position that PG,
  failing inside `eval_const_expressions`, does not print.
