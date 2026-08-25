# PERF-BASELINE — parser rewrite micro-benchmarks

Recorded at P0 (2026-08-25, AMD Ryzen 7 5700X, go1.26 toolchain,
`GOMEMLIMIT=15GiB`, `-benchtime 1s`). Regenerate with:

```bash
go test ./internal/sqlparser/ -bench . -benchmem -run '^$'
```

Gate (04-testing-and-gates.md §3): every wave flip re-runs the suite;
>2x regression vs the CURRENT row for any input class stops the flip.
Update this file whenever the baseline legitimately moves (cite reason).

## Input classes

| class | fixture |
|---|---|
| select-heavy | TPC-H-shaped join/aggregation query (`benchSelect` in bench_test.go) |
| ddl-heavy | CREATE TABLE w/ 5 constraints (`benchDDL`) |
| expr-heavy | nested boolean/arithmetic + CASE + IN/LIKE (`benchExpr`) |

## P0 skeleton (new-parser path)

The skeleton grammar accepts only empty input, so these measure the
adapter+error path over the full token stream — a floor, not a comparison
point. Real comparison begins at P1 flips.

| benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| SkeletonParseOneSelectHeavy | ~1012 | 2586 | 6 |
| SkeletonParseOneDDLHeavy | ~1013 | 2586 | 6 |
| SkeletonParseOneExprHeavy | ~1043 | 2586 | 6 |

## Legacy recursive-descent (comparison points)

| benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| LegacyParseSelectHeavy | 8590 | 3659 | 93 |
| LegacyParseDDLHeavy | 6049 | 4996 | 57 |
| LegacyParseExprHeavy | 7708 | 2978 | 91 |

Notes:

* The new parser's totals must include its own lexing-equivalent work to be
  comparable (the adapter consumes pre-split slices; dispatch lexing happens
  once per batch — accounted at the Parse() layer from P1 onward).
* Timing hygiene per repo guidance: same machine state for before/after
  pairs; never compare across CPU-frequency regimes.
