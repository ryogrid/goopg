# Partitionwise aggregation/join — scoping recon (AI-20260922-004850-016)

Status: RECON COMPLETE (2026-09-22). No production file touched. The task is
`[!]` pending an OWNER DECISION on an inventory row — see §4.
Kind: recon
Parent: none
Movement: none

## 1. What prompted it

The nightly filed `testport/TestPort_RegressSuite` with four failing
subtests. Three (`select_having`, `select_implicit`, `union`) were one
bpchar-padding root cause and were fixed in the previous loop. The fourth,
`partition_aggregate`, was split out because it is a different class
entirely. This document sizes it.

## 2. It is not a regression, and it never passed

Measured rather than assumed. The case was built and run in an isolated
worktree at `aef4a4257` — the commit *before* M0143-0007b slice 1, i.e.
before any of the bpchar work the nightly's own sha (`c07ebf011`) pointed
at. It fails there with the byte-identical message:

```
regress case partition_aggregate (must-pass) diverged:
output mismatch; normalization rules need extension
```

So goopg's behaviour is identical at both commits. The nightly's
`first-seen: 20260922 (new tonight)` label is not explained by a code
change; the 20260921 run simply did not report this subject. That label is
left unexplained here rather than guessed at — what matters for scheduling
is that no recent change caused it.

## 3. The real size: the optimizer is not partition-aware

Two facts, both checked directly:

1. **The case needs two features, not one.** The upstream file sets
   `enable_partitionwise_aggregate TO true` (line 10) *and*
   `enable_partitionwise_join TO true` (line 12), then runs 115 queries
   under both. goopg declares both GUCs in
   `internal/catalog/catalog.go:12302-12306`, with PostgreSQL's own `off`
   default — and nothing consumes either. They are
   declared-but-unconsumed.

2. **Partition metadata never reaches the planner.** `catalog.Table` carries
   `PartitionKey`, `PartitionMethod` and `PartitionBounds`
   (`internal/catalog/catalog.go:649-660`). Every reader of those fields
   lives in `internal/executor`; `internal/optimizer` never reads them.

So this is not "add a grouping variant". The planner has no representation
of a partitioned relation at all, and that has to exist before either
feature can be written. goopg plans these queries as `HashAggregate` over an
`Append` of child scans, which is the correct plan for
`enable_partitionwise_aggregate = off` — i.e. goopg is self-consistent, just
missing the feature the test enables.

## 4. The blocking issue is governance, not engineering

`docs/test-port/postgres-oracle-target-inventory.csv` line 139 marks this
case `status=pass, pass_required=yes`. In this repo's status vocabulary
`pass` means "regress/isolation case passing; must stay passing". The same
row's rationale says "output diverges from expected", and §2 shows the
rationale is the accurate half.

The suite's must-pass set is exactly the rows with `status=pass`
(`regressMustPass`, `internal/testport/regress_suite_test.go:182`), so this
single row keeps a pass-required gate permanently red against a feature
nobody has built.

**The loop did not demote the row.** Doing so is the obvious way to turn the
gate green, which is exactly why it is not the loop's call: changing a
case's must-pass status is a governance decision, and the documented
promotion workflow only covers promotion. The owner's options:

- **(i)** correct the row to `status=failed` — the vocabulary's "in-scope
  case, currently diverging", which is what the measurement supports and
  what comparable in-scope-but-diverging cases already use; or
- **(ii)** keep it must-pass and schedule §5 as real work.

Checked for a systematic defect before escalating: 21 rows carry
`status=pass` with a stale "output diverges" rationale, but the full regress
suite is green on all of them except this one. For the other 20 the STATUS
is correct and only the rationale TEXT is stale. This is a single-row
defect, not a consolidation-wide one.

## 5. Decomposition, if the answer is "build it"

Deliberately NOT filed as tasks — filing them would presume option (ii).

| step | work | PG oracle |
|---|---|---|
| (a) | surface partition metadata onto the optimizer's rel representation | `PartitionScheme` / `RelOptInfo.part_scheme` |
| (b) | partition-bound matching between two rels | `partition_bounds_equal` |
| (c) | partitionwise join | `try_partitionwise_join`, joinrels.c |
| (d) | partitionwise grouping | `create_partitionwise_grouping_paths`, planner.c |

(b) is the piece (c) and (d) share, so it is the natural second step.

## 6. A smaller divergence found while reading, deliberately not bundled

goopg's EXPLAIN renders a numeric constant as `Filter: (avg(pagg_tab.d) <
15)` where PG renders `< '15'::numeric`. It is a genuine rendering
divergence and much smaller than the above — but fixing it alone would NOT
make this case pass, because the plan SHAPE still differs. Recorded so a
later loop does not mistake it for the whole gap, and so it is not lost.
