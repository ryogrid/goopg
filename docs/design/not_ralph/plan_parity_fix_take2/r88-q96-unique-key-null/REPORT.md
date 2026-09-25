# R88 REPORT — bare unique keys retain ordinary null-aware equality

Design: [SCOPE.md](SCOPE.md). Implementation commit: `0b98709ae`.

## Result

R88 makes the required distinction in both cardinality coordinate spaces:

* a non-partial bare unique index proves only the sound output-row ceiling;
* a declared FK alone consumes equality clauses and contributes
  `1/ref_tuples`; and
* completed-plan no-MCV equality now applies both strict-operator null
  complements exactly once. The existing two-sided MCV branch remains
  exclusive.

The Q96 source correction substantially narrows the nullable bare-unique
estimates but does not select the PG prefix. The trace retains a 12/13-row
difference from the PG/source-rule L2 figures, documented below rather than
claimed away. This was an expected possible outcome: R87 attributed most of
the old L2 gap to faithful startup/cost terms, not the roughly 1.76-unit
null-fraction correction.

## Implementation and review

`joinrelsize.go` and `joinkeyproof.go` now represent FK consumption and a
structural bound independently. Bare unique candidates are enumerated over the
intact equality set, retaining the minimum opposite-side post-filter bound;
this avoids selecting by raw tuples and missing a tighter bound when both sides
are unique. The FK pass then sees the original clauses and continues to choose
its largest raw referenced divisor.

The implementation review initially found two blockers, both fixed before its
final approval:

1. clause-private candidate tracking could hide the tighter of two valid
   bare-unique bounds; and
2. overlapping FK/UNIQUE tests had the same final row count even if FK
   consumption was accidentally removed.

The resulting pins cover nullable and non-null bare keys, both coordinate
spaces, overlapping FK plus parent UNIQUE state, fully covered default-NDV
composites, both-MCV null handling, partial-index decline, partial composite
decline, and the filtered two-unique-side bound selection.

## Q96 measurement

The committed binary was `/tmp/pp2/r88/goopg-r88`
(`sha256=005dfce99f3417a2af619b37fb8d9b3ea8c262b73fac1e3c90dcbf5c747dcc34`).
A fresh private copy of the R87 SF0.25 cluster ran on `127.0.0.1:5560` with
`work_mem=64MB`, four maximum parallel workers, and leader participation.
Both natural trace A/A captures are byte-identical. The `GATHER_PATHS=top`
run was diagnostic only and selected the same natural plan.

The diagnostic partial tournament supplies the relevant same-relset evidence.
All shown paths are partial Hash Join paths with zero pathkeys and zero
disabled nodes. `inputtotal` is the partial outer input cost.

| level / candidate | equality inputs | rows | startup / total | `inputtotal` | verdict |
| --- | --- | ---: | ---: | ---: | --- |
| L2 `ss ⋈ hd` | outer 232,218; filtered inner 720 / raw 7,200; NDVs 7,068 vs 7,200; null fractions .044066668 and 0 | 22,186 | 149.00 / 16,497.86 | 15,256.18 | accepted |
| L2 `ss ⋈ store` | outer 232,218; filtered inner 1 / raw 12; NDVs 6 vs 12; null fractions .0438 and 0 | 18,491 | 1.16 / 16,313.07 | 15,256.18 | accepted, lower by 184.79 |
| L3 PG-shaped `(ss ⋈ hd) ⋈ store` | above L2 candidate plus filtered store | 1,767 | 150.16 / 16,599.89 | 16,497.86 | accepted |
| L3 retained `(ss ⋈ store) ⋈ hd` | above L2 survivor plus filtered household demographics | 1,767 | 150.16 / 16,549.08 | 16,313.07 | accepted, lower by 50.81 |

The trace’s integer L2 values are 22,186 / 18,491. With the displayed rounded
outer rows and PG statistics, the direct source-rule products are 22,198.49
and 18,503.90 (rounding to the R87/PG-source predictions 22,198 and 18,504).
The observed Goopg values are thus lower by about 12.49 and 12.90 rows. That
remaining difference is in a Goopg input below the displayed trace precision
or its internal rounding path; R88 neither masks it nor attributes it to a
declared FK. Live PG’s selected `ss ⋈ hd` is 22,198.

The natural serial plan still reports `ss ⋈ store` as 57,322 rows at total
cost 23,406.68, then `(ss ⋈ store) ⋈ hd` as 5,477 rows at 23,825.41; these are
not the partial-list comparison that decides the PG-shaped prefix tournament.
PG’s selected parallel L3 is `(ss ⋈ hd) ⋈ store`, 1,769 rows at 16,099.21,
and its final nested loop is 35 rows versus Goopg’s serial 74 rows.

Goopg therefore retains `((store_sales ⋈ store) ⋈
household_demographics) ⋈ time_dim`; PG chooses
`((store_sales ⋈ household_demographics) ⋈ store) ⋈ time_dim` below its
parallel aggregate. No tree was forced.

Q96 values agree exactly: both engines return `266`.

## Blast radius and corpus gates

Q9 is the explicit blast-radius witness. Its trace-on/off EXPLAIN is
byte-identical, and the live-PG normalized census reports Q9 `MATCH`. The
global estimator change also passed all mandatory value gates:

* `go test ./internal/optimizer ./internal/executor ./internal/testutil/estimateaudit`
  and the matching `go vet` invocation: PASS.
* trace-on/off byte controls for Q9/Q41/Q91/Q96: all four PASS.
* TPC-H private-clone digest: 24/24 MATCH, value verdict PASS.
* TPC-DS SF0.25 sweep: `PASS=96` (60 checksum verified, 36 row-count-only),
  `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3`.
* Fresh live-PG SF0.25 census: `match=2 shapediff=67 unparsed=0
  missingnode=27 error=3 timeout=0`; Q96 remains a shape difference. Relative
  to the retained R85 live capture (`2/69/0/25/3/0`), this is two fewer
  shape differences and two more missing-node classifications, not an
  “unchanged” census claim.

## Evidence

All run artifacts are outside the repository under `/tmp/pp2/r88/`:

* `q96-natural-a.txt`, `q96-natural-b.txt`, `q96-top.txt`,
  `q96-pg-live.txt`, and the corresponding server trace logs;
* `q96-goopg-value.txt`, `q96-pg-value.txt`, and `q96-key-stats.txt`;
* `trace-{on,off}-q{9,41,91,96}.txt`;
* `tpch-digest.txt` and `tpch-digest-diff.txt`;
* `sf025-results/sweep-20260912-110451.txt` and its plan capture;
* `ds-pg-live.txt` and `ds-parity-live.txt`.

The SF0.25 script writes `=====` query headers while
`pg-plan-parity-diff.py` requires `===`; `ds-goopg-parity-input.txt` is the
header-only compatibility normalization used for that census. It does not
change plan content.

## Next divergence

R88 closes the conflation of bare unique evidence with FK selectivity. Q96's
earliest remaining measured boundary is the same-relset partial tournament:
the `ss ⋈ store` L2 candidate is cheaper by 184.79 and its L3 continuation by
50.81, so it survives over the PG-shaped `ss ⋈ hd` prefix before any upper
Gather/serial comparison. A later round must scope that partial-prefix cost
boundary first; downstream parallel admission is conditional on its result.
It must not undo R88's source-faithful equality treatment.
