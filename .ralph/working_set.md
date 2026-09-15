Task: M0138-0007 — give `numeric` columns a real `avg_width`. **DONE this
loop** (`3a518d693`, committed). Banner's item 3 (M0138 0007-0009) has one
task left: **M0138-0008** (bisect the category shift M0138 caused) and
**M0138-0009** (re-open correlation banding) are both still `[ ]`.
M0140-0006 (TPC-DS parallelism) is also still open under item 3.

Files: `internal/executor/operators_analyze.go` (new
`numericFastPathOnDiskWidth` + `pgVarlenaShortMaxBody`, wired into
`datumVariablePayloadWidth`'s `KindNumeric` fast-path arm; new `internal/nodes`
import), `internal/executor/operators_analyze_test.go` (new
`TestNumericFastPathOnDiskWidth`, updated `TestDatumVariablePayloadWidth`'s
numeric-fast case from `0`→`7`), `internal/executor/spill_test.go` (removed
`fastNum` from `TestEstimatedRowBytesCountsEnumAndBigNumeric`'s cross-check
loop — documented as an intentional divergence, not a regression),
`docs/design/0100-0149/m0138-0007-numeric-avg-width.md` (new),
`docs/design/README.md` (+1 row), `.ralph/fix_plan.md` (M0138-0007 `[x]`),
`.ralph/deferral_ledger.md` (`m0138-0005` numeric-avg_width row flipped
`resolved`).

Key symbols: `numericFastPathOnDiskWidth(mantissa int64, scale int16) int`
(operators_analyze.go) — formats via the existing `formatNumeric`, encodes via
`internal/nodes.NumericBodyFromText` (the same numeric_in port `codec.go` uses
for the heap's on-disk numeric form), wraps in a 1-or-4-byte varlena header
per `pgVarlenaShortMaxBody` (= PG's `VARATT_SHORT_MAX - VARHDRSZ_SHORT`).

Findings: the ledger row's own guessed formula ("`NUMERIC_HDRSZ` plus digits")
was **wrong** — that's `numeric_maximum_size`'s typmod-derived WORST CASE
(already used, correctly, for a different purpose — the planner's row-width
upper bound in `internal/optimizer/relsize.go`'s `numericHeaderSize`), not
what `compute_scalar_stats` actually measures (`VARSIZE_ANY` of the raw
on-heap Datum, header included). Caught this by checking against the live PG
18.3 TPC-H reference (`:65432`) BEFORE committing to a formula:
`pg_column_size(l_quantity)=5` for `18`, `pg_stats.avg_width=8` for
`l_extendedprice` — both reproduce exactly with the short-varlena-header
formula, would NOT reproduce with the long-header one (off by 3-4 bytes/row).
Sibling-path check done: `spill.go`'s `estimatedRowBytes` intentionally stays
at `+0` for the numeric fast-path arm (in-memory footprint, not on-disk width
— its own doc comment already says the two rulers "differ by up to 5x"); its
test had a coincidental (both-0) equality assertion for this one case, fixed
with an explanatory comment rather than silently broken or force-matched.

In-flight: none. No bench servers touched (used the already-running
`:65432`/`:65433` shared TPC-H lanes read-only for the oracle spot-check and
`scripts/tpch-spotcheck.sh`, which clones a private snapshot rather than
restarting the shared pair).

Next step: re-read the `## Current Priority` banner fresh before trusting
this note. As last read this loop, item 3 is: M0138-0008 (bisect the
join-order/qual-placement category shift M0138 caused — resume point:
re-capture TPC-DS plans at the commit before M0138-0002 landed and diff
against HEAD via `scripts/pg-plan-parity-diff.py --verbose`) or M0138-0009
(re-open the correlation-banding finding — resume point: build a synthetic
table with a hand-constructed physical-position/value relationship to
distinguish a goopg bug from a TPC-DS-loader physical-layout artifact,
per the `m0138-0001` ledger row's most recent resume note) or M0140-0006
(TPC-DS parallelism partial-Append producer). Any of the three is a
reasonable pick under item 3; M0138-0009's synthetic-table approach is
probably the best-scoped next slice since it has the most concrete resume
point already written.

Gates run: `go build ./...` clean. `go test ./internal/executor/...
./internal/optimizer/...` full package green (new/updated tests pass).
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: green except
the pre-existing, already-filed `internal/parser` `GroupedJoinUnaliased`
AST-drift (60 test functions — confirmed unrelated, same failure pre-existed
before this loop's changes). `scripts/tpch-spotcheck.sh` `RESULT=PASS`
(Q12=2/Q13=34, canonical anchors). Pre-commit hook's pgbench smoke PASS
(tps ~42-149, 0 failed). `make ralph-state-guard`: one self-repair (same
recurring benign stale-clean-exit-marker pattern several prior loops have
noted), clean after repair.
