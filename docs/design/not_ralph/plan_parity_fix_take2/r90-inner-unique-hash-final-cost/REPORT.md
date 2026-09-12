# R90 REPORT — inner-unique hash final-cost input

R90 implements the bounded C2 follow-up from R89. Production commit
`b2b671e7f` carries fail-closed bare-unique inner evidence to Hash Join final
costing; follow-up test commit `3469163a6` directly proves that matching
partial unique indexes decline that input. Neither invents PostgreSQL's
missing virtual-bucket input or forces Q96's join order.

## Implementation

`hashJoinFinalCostInputFor` proves an INNER-only input when the complete,
unparameterized build side is one base relation and the hash-clause columns
collectively cover a complete non-partial bare unique index. It reuses
`provableKeys`, rejects FK evidence, and declines expressions, incomplete
composites, joined inners, parameterized inners, LEFT, SEMI, and ANTI joins.
The total-relation `joinrel.Rows / outer.Rows` match fraction is shared by the
serial and partial siblings; each candidate alone rounds its own outer rows
with `math.RoundToEven`.

For a proved input, matched probes use the existing inner bucket statistic and
the `2 / (1 + match_count)` shape with `match_count = 1`. An unmatched Goopg
canonical-map key has no candidate slice to walk, so its tuple-walk charge is
zero. This is an explicit executor-coordinate adaptation: `NBuckets` and
`NBatch` remain map sizing/spill inputs and are not substitutes for PG's
`virtualbuckets`.

The remaining PG inputs are intentionally still absent: virtual-bucket walking,
packed-tuple geometry, MCV-frequency suppression, general `QualCost`, and
pathtarget costs. No constants were introduced for them.

Focused tests pin complete bare-composite proof, expression/partial/incomplete/
joined/parameterized declines, two-sided orientation independence, LEFT/SEMI/
ANTI and no-stats preservation, half-to-even rounding, zero unmatched walk,
and path-level serial/partial routing with one residual charge. The latter
asserts a 25-row partial outer, a 40-row complete inner, and exact costs at
both producer sites.

## Q96 and controls

The final Q96 controls used `/tmp/pp2/clone-ds025-r90` on `127.0.0.1:5560`
and the binary built from committed source `3469163a6`,
`/tmp/pp2/r90/goopg-3469163a6`, SHA-256
`7eb76e8c68b593ec56a6fa1419b0c6381d49d8c5ae47db6e5016bc9e2ed65938`.
The four transient-unit definitions, after-stop status records, and journals
are retained with the captures, so the unset-`Gather`, `top`, and trace
environments are auditable. The PG18.3 oracle remained
`127.0.0.1:65438/tpcds025`.

Q96 preserves its result value: Goopg and PG both return `266`. With
`GOOPG_GATHER_PATHS` unset, natural A/A plans are byte-identical, SHA-256
`7d36b577372f63e6e44d139699e052b74f499f9b949f8b946395dcfae0028ead`.
The separate `GOOPG_GATHER_PATHS=top` trace-off capture is byte-identical to
natural; its trace-on capture is also byte-identical. Trace on/off is likewise
byte-identical for Q9, Q41, and Q91. The Q9, Q41, Q91, and Q96 hashes,
respectively, are:

* `4392109fd377ed7a62afd9bd2c38f1f2b19c4453a064c4a680c606f9a41df5f6`
* `660660223437562f669c49a98e0665085fbba7880565c870e4f7733e6306ef76`
* `106c24f5a7b52d32f06888af9f64ebaf462b9a8b44434b6ac658bda94ce355ce`
* `7d36b577372f63e6e44d139699e052b74f499f9b949f8b946395dcfae0028ead`

The natural Q96 tree is still the serial
`(store_sales -> store) -> household_demographics` prefix, not PG's partial
`(store_sales -> household_demographics) -> store`. Its top cost moved from
R89's `25434.49` to `24541.49`; this is not a promised order flip and no
preference was added.

## Gates and evidence

All required local suites pass:

```
go test ./internal/optimizer ./internal/executor ./internal/testutil/estimateaudit
go vet  ./internal/optimizer ./internal/executor ./internal/testutil/estimateaudit
git diff --check
```

The final-source TPC-H digest is 24/24 MATCH with verdict PASS. The final-source
SF0.25 sweep is
`PASS=96`, `MISMATCH=0`, `CKMISMATCH=0`, `ERROR=0`, `TIMEOUT=0`, `SKIP=3`.
Its capture and a fresh live-PG plan census give:
`match=2 shapediff=67 unparsed=0 missingnode=27 error=3 timeout=0`; Q96 is a
shape difference.

All run artifacts are outside the worktree under `/tmp/pp2/r90/`, including
the final natural A/A and named top trace-off Q96 captures, unit definitions,
after-stop status records/journals, and trace controls,
`tpch-digest-3469163a6.txt`, `tpch-digest-3469163a6-diff.txt`, the final
SF0.25 sweep and plan capture in `sf025-results-3469163a6/`,
`ds-pg-live-3469163a6.txt`, and `ds-parity-live-3469163a6.txt`.

## Next boundary

R90 establishes the sound inner-unique carrier and executor-coordinate matched
probe price, but it cannot reproduce PG's unmatched virtual-bucket term without
a new represented, source-compatible geometry input. Q96 did not flip, so any
next round must measure that remaining term (and the already-recorded PG
MCV/QualCost/pathtarget gaps) before authorizing another cost change.
