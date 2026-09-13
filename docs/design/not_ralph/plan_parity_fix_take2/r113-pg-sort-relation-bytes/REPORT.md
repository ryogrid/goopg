# R113 — PG relation-byte Sort-price comparison: report

## Outcome

R113 implemented the approved, default-off
`GOOPG_PG_SORT_RELATION_BYTES_COST=1` planner experiment. It replaces only
`costSortRun` external-sort input/output volume with PG18.3
`relation_byte_size(rows, PathTarget.width)`, while retaining Goopg entry bytes
for switch-off costing and every executor allocation.

The PG representation reached real disk/memory boundaries but changed no
selected plan in either corpus. It also preserved all measured values. Keep the
switch default-off: this isolated cost term is not a parity-improving election
lever in the measured candidates.

## Implementation proof

The opt-in branch uses 8-byte `MAXALIGN` and the PG18 24-byte
`SizeofHeapTupleHeader`. It follows `cost_tuplesort`'s asymmetric small-input
ordering: input bytes are calculated before the two-row floor; bounded output
bytes are calculated after the floor and LIMIT decision. Invalid, unknown, or
overflowing width fails closed to the legacy Goopg entry-byte price.

All production Sort callers carry emitted width: `sortPathForBounded` uses
`pathWidth(sub)` (merge inputs and ORDERED paths), both partial-sort arms use
`ordered.Width` from `child.Output()`, and WindowAgg receives its pre-window
child width. A source-wide test rejects any new legacy direct caller and pins
the complete width-aware caller set. Focused tests cover alignment/overflow,
floor and bounded boundaries, switch-off identity, narrow output width,
ORDERED/partial/window provenance, trace content, and executor isolation.

The implementation and focused proof were reviewed and approved before commit
`104e9bdb5` (`experiment(planner): add PG relation-byte sort pricing`).

## Measurements

All captures used `/tmp/r113-goopg`, built from `104e9bdb5`, with PG-shaped DP
and EXPLAIN-only plan capture unless stated otherwise. Temporary artifacts are
under `/tmp/r113-*` and are not repository fixtures.

| check | OFF | ON | result |
|---|---:|---:|---|
| TPC-H plan-only forms | 22 | 22 | normalized structures identical; a second switch-off A/A capture is also identical |
| TPC-DS SF0.25 plan capture | 99 | 99 | normalized structures identical; switch-off A/A is also identical |
| TPC-H digest | 24 `OK` records | 24 `OK` records | row counts and ordered/unordered hashes identical (elapsed excluded) |
| TPC-DS SF0.25 values | `PASS=96`, `MISMATCH=0`, `CKMISMATCH=0`, `ERROR=0`, `TIMEOUT=0`, `SKIP=3` | same | no value/status movement |
| live PG18.3 TPC-DS SF0.25 census | `match=2`, `shapediff=69`, `unparsed=0`, `missingnode=25`, `error=3`, `timeout=0` | same | no structural-parity movement |

The live reference was PostgreSQL 18.3 on `127.0.0.1:65438`, database
`tpcds025`, with `work_mem=64MB` and `max_parallel_workers_per_gather=4`
pinned through `PGOPTIONS`; it ran EXPLAIN only. The paired Goopg/PG reports
are `/tmp/r113-ds-parity-off.txt` and `/tmp/r113-ds-parity-on.txt`.

`DPPGSORT` proves the comparison was not unreached. TPC-H costed 2,530 Sort
candidates in each arm. In the retained OFF trace, Goopg classified 1,296 as
disk and 1,234 as memory, while the simultaneously computed PG representation
classified 1,341 as disk and 1,189 as memory. TPC-DS costed 78,980 Sort
candidates in each arm. There, Goopg classified 6,610 disk, 72,361 memory, and
9 bounded; PG classified 3,679 disk, 75,292 memory, and 9 bounded. PG bytes
therefore changed reached branch classifications, but surrounding path
alternatives did not cross over.

Commands used for the main OFF/ON evidence:

```text
GOOPG_BIN=/tmp/r113-goopg AUDIT_BIN=/tmp/r113-estimate-audit NO_BUILD=1 \
PLAN_ONLY=1 PGSHAPED=1 DP_TRACE=1 REFERENCE= \
scripts/tpch-estimate-audit-arm.sh r113-off-tpch --out /tmp/r113-off-tpch

GOOPG_PG_SORT_RELATION_BYTES_COST=1 GOOPG_BIN=/tmp/r113-goopg \
AUDIT_BIN=/tmp/r113-estimate-audit NO_BUILD=1 PLAN_ONLY=1 PGSHAPED=1 \
DP_TRACE=1 REFERENCE= scripts/tpch-estimate-audit-arm.sh r113-on-tpch \
--out /tmp/r113-on-tpch

GOOPG_BIN=/tmp/r113-goopg SF025_NO_BUILD=1 \
SF025_RESULTS_DIR=/tmp/r113-off-ds scripts/tpcds-sf025-regression.sh sweep

GOOPG_PG_SORT_RELATION_BYTES_COST=1 GOOPG_BIN=/tmp/r113-goopg \
SF025_NO_BUILD=1 SF025_RESULTS_DIR=/tmp/r113-on-ds \
scripts/tpcds-sf025-regression.sh sweep
```

## Reproducibility ledger

The following retained rerun is the trace evidence for the reached-candidate
claim above. It deliberately uses new output names so a reviewer need not rely
on a rotating server log. Field-safe extraction reported the TPC-H breakdown
`Goopg: 1296 disk, 1234 memory; PG: 1341 disk, 1189 memory` for both arms, and
the TPC-DS breakdown `Goopg: 6610 disk, 72361 memory, 9 bounded; PG: 3679 disk,
75292 memory, 9 bounded` for both arms. Each breakdown sums to its respective
`DPPGSORT` total (2,530 TPC-H; 78,980 TPC-DS). The retained artifacts are
`/tmp/r113-retained-{off,on}-tpch.trace.log` and
`/tmp/r113-retained-{off,on}-ds.trace.log`.

```text
GOOPG_BIN=/tmp/r113-goopg AUDIT_BIN=/tmp/r113-estimate-audit NO_BUILD=1 \
PLAN_ONLY=1 PGSHAPED=1 DP_TRACE=1 REFERENCE= \
scripts/tpch-estimate-audit-arm.sh r113-retained-off-tpch \
  --out /tmp/r113-retained-off-tpch
cp tmp/tpch-audit-r113-retained-off-tpch.server.log \
  /tmp/r113-retained-off-tpch.trace.log

GOOPG_PG_SORT_RELATION_BYTES_COST=1 GOOPG_BIN=/tmp/r113-goopg \
AUDIT_BIN=/tmp/r113-estimate-audit NO_BUILD=1 PLAN_ONLY=1 PGSHAPED=1 \
DP_TRACE=1 REFERENCE= scripts/tpch-estimate-audit-arm.sh \
  r113-retained-on-tpch --out /tmp/r113-retained-on-tpch
cp tmp/tpch-audit-r113-retained-on-tpch.server.log \
  /tmp/r113-retained-on-tpch.trace.log

for f in /tmp/r113-retained-{off,on}-tpch.trace.log; do
  rg -c '^DPPGSORT' "$f"
  rg '^DPPGSORT' "$f" | sed -E 's/.* goopgbranch=([^ ]+).*/\1/' | sort | uniq -c
  rg '^DPPGSORT' "$f" | sed -E 's/.* pgbranch=([^ ]+).*/\1/' | sort | uniq -c
done

source bench/tpcds/env_tpcds.sh
r113_log_start=$(wc -l < "$SF025_LOG")
GOOPG_PGSHAPED_DP_TRACE=1 GOOPG_BIN=/tmp/r113-goopg SF025_NO_BUILD=1 \
SF025_RESULTS_DIR=/tmp/r113-retained-off-ds SF025_PLANS_BASELINE=none \
scripts/tpcds-sf025-regression.sh plans
sed -n "$((r113_log_start + 1)),\$p" "$SF025_LOG" \
  > /tmp/r113-retained-off-ds.trace.log
# Repeat with GOOPG_PG_SORT_RELATION_BYTES_COST=1 and r113-retained-on-ds.
for f in /tmp/r113-retained-{off,on}-ds.trace.log; do
  rg -c '^DPPGSORT' "$f"
  rg '^DPPGSORT' "$f" | sed -E 's/.* goopgbranch=([^ ]+).*/\1/' | sort | uniq -c
  rg '^DPPGSORT' "$f" | sed -E 's/.* pgbranch=([^ ]+).*/\1/' | sort | uniq -c
done
```

Plan-shape comparison removed only `cost=`, `rows=`, and `width=` before
`cmp`; the OFF/ON and switch-off A/A outputs were identical. The commands and
normalized artifacts were:

```text
sed -E 's/  \(cost=[^)]* rows=[^)]* width=[0-9]+\)//' \
  /tmp/r113-off-tpch/r113-off-tpch.plans.txt > /tmp/r113-off-tpch-shape.txt
sed -E 's/  \(cost=[^)]* rows=[^)]* width=[0-9]+\)//' \
  /tmp/r113-on-tpch/r113-on-tpch.plans.txt > /tmp/r113-on-tpch-shape.txt
cmp -s /tmp/r113-off-tpch-shape.txt /tmp/r113-on-tpch-shape.txt
# The same commands with r113-aa-tpch produced the A/A comparison.

sed -E '1,7d; s#/tmp/r113-(off|on|aa)-ds/\.explain_all\.sql#/tmp/CONTROL/.explain_all.sql#g; s/  \(cost=[^)]* rows=[^)]* width=[0-9]+\)//' \
  /tmp/r113-off-ds/plans-20260913-162423.txt > /tmp/r113-off-ds-shape.txt
sed -E '1,7d; s#/tmp/r113-(off|on|aa)-ds/\.explain_all\.sql#/tmp/CONTROL/.explain_all.sql#g; s/  \(cost=[^)]* rows=[^)]* width=[0-9]+\)//' \
  /tmp/r113-on-ds/plans-20260913-162439.txt > /tmp/r113-on-ds-shape.txt
cmp -s /tmp/r113-off-ds-shape.txt /tmp/r113-on-ds-shape.txt
# Substitute /tmp/r113-aa-ds/plans-20260913-164133.txt for A/A.
```

The TPC-H value arms were run with `scripts/tpch-acceptance-arm.sh` (OFF:
`r113-off /tmp/r113-off-tpch-values.txt`; ON: the same invocation with
`GOOPG_PG_SORT_RELATION_BYTES_COST=1`, `r113-on`, and
`/tmp/r113-on-tpch-values.txt`). Both used `GOOPG_BIN=/tmp/r113-goopg`,
`NO_BUILD=1`, `PGSHAPED=1`, `COLLAPSE=1`, `GOGC=100`, and
`GOMEMLIMIT=12GiB`. Normalizing `elapsed=[0-9.]+s` with `sed -E` then produced
an empty diff. The TPC-DS commands shown above ran `sweep` rather than `plans`;
their retained results are `/tmp/r113-off-ds/sweep-20260913-162834.txt` and
`/tmp/r113-on-ds/sweep-20260913-163157.txt`.

For the live census, `bench/tpcds/server.sh start pg` started the existing
reference cluster, then `source bench/tpcds/env_tpcds.sh` supplied host, port,
user, database, and query directory. Each `query{1..99}.sql` was split into
statements and sent to `psql` as `EXPLAIN` with
`PGOPTIONS='-c work_mem=64MB -c max_parallel_workers_per_gather=4'`; the raw
capture is `/tmp/r113-ds-pg-live.txt`. After converting Goopg headers with
`sed -E 's/^===== Q([0-9]+) =====$/=== Q\1/'`, the two commands below created
the cited reports:

```text
python3 scripts/pg-plan-parity-diff.py \
  /tmp/r113-ds-goopg-off.sections.txt /tmp/r113-ds-pg-live.txt \
  > /tmp/r113-ds-parity-off.txt
python3 scripts/pg-plan-parity-diff.py \
  /tmp/r113-ds-goopg-on.sections.txt /tmp/r113-ds-pg-live.txt \
  > /tmp/r113-ds-parity-on.txt
```

## Decision

Do not promote the Sort switch and do not use its PG tuple representation for
HashAggregate, Memoize, scans, indexes, materialization, or executor memory.
R112's next independently reached candidate is HashAggregate, but it requires
its own planner/executor correspondence scope before any code experiment.
