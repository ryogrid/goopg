# sum and avg over interval (M0149-0001h1a79)

## Problem

`select sum(i), avg(i) from (values ('1 hour'::interval),('2 hours')) v(i)` raised
`aggregate sum requires numeric argument in v0`. PG 18.3 returns `03:00:00` and `01:30:00`.

## PG behaviour

timestamp.c keeps an IntervalAggState: N, the count of finite inputs; sumX, their sum; and the counts of
+infinity and −infinity inputs.

- interval\_accum adds a finite input with finite\_interval\_pl, which raises 22008 on overflow, and counts an
  infinite one by its sign.
- interval\_sum and interval\_avg return infinity when the infinities all have one sign, and 22008
  `interval out of range` when both signs occur.
- Otherwise interval\_sum returns sumX, and interval\_avg returns interval\_div\(sumX, N\).
- interval\_combine merges two states for parallel aggregation.

## Design

The interval state reuses fields the sum/avg state already has, so state serialization \(agg\_state\_serial.go\)
and the parallel combine need no new fields:

- `numericSum` holds the finite interval sum. A KindInterval numericSum marks the state as an interval one; an
  infinite first input seeds it with a zero interval.
- `floatSpecial` holds the infinity signs. Its combine rule turns +inf plus −inf into NaN, and NaN on an interval
  state raises 22008. The transition raises it as soon as the second sign appears; PG raises it at the final
  function. Both raise it for the same group.
- `count` is N. With an infinity present the result is infinite, so the count is not used then.
- `finishIntervalSumAvg` in operators\_join\_agg.go is the final function. `combineNumericSum` adds interval sums
  \(interval\_combine\).
- The analyzer accepts an interval argument for sum and avg. avg\(interval\) is typed interval in the analyzer, in
  planner exprType and for window functions.

## Verification

- Results match PG for finite sums, NULLs, infinities, empty input, GROUP BY, DISTINCT, window frames, overflow,
  a forced parallel plan \(Partial and Finalize Aggregate\) and a spilling hash aggregate at work\_mem=64kB.
- TestIntervalSumAvg in internal/executor/interval\_sum\_avg\_test.go.

## Not done

- Moving\-aggregate inverse transitions \(interval\_accum\_inv\) for window frames: goopg recomputes each frame.
- interval input overflow is a separate defect, M0149\-0001h1a79a \(S2\).
