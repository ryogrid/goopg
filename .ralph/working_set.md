Task: M0134-0053 (partition_prune.sql) — PARTIAL this loop. Landed a real
HASH multi-column routing bugfix from sizing; case itself stays `failed`
(pruning subsystem PARKED). CSV row unchanged. Next: select M0134-0054
(plancache.sql).

Files this loop: `internal/executor/hash_partition.go` (new shared helper
`computeHashPartitionRowHash`), `internal/executor/expr.go`
(`satisfies_hash_partition` now calls the shared helper),
`internal/executor/operators_storage.go` (`routeToPartitionDepth`'s HASH
case now folds ALL partition-key columns via the shared helper + routes via
`FindHashPartitionByHash` uniformly), `internal/executor/
hash_partition_multicol_routing_test.go` (new, 2 tests), `.ralph/
deferral_ledger.md` (new row, M0134-0053 bucket breakdown), `.ralph/
fix_plan.md` (M0134-0053 entry rewritten with PARTIAL verdict + next-task
pointer), `.ralph/progress.json` (state-guard auto-repair, recurring).

Key symbols: `computeHashPartitionRowHash` (hash_partition.go, new),
`routeToPartitionDepth` (operators_storage.go), `satisfies_hash_partition`
case in expr.go's evalExpr dispatch, `im.FindHashPartitionByHash`
(catalog.go:5036, untouched — confirmed already PG-faithful modulus match).

Hypothesis/Findings: partition_prune.sql's dominant gap (~85-90% of a
6417-line diff) is that partition pruning — BOTH planner-time (static,
constant-folded WHERE clauses collapsing an Append to matching children)
and executor-time (runtime pruning via InitPlan/param bounds, "Subplans
Removed: N" output) — is entirely unimplemented anywhere in
`internal/optimizer`/`internal/executor`. This is the SAME missing
foundation class as M0134-0052's partition-wise-join gap: goopg's
partitioning support handles DDL/routing but has zero plan-shape awareness
of partition bounds. Two more independent contained bugs were sized but
NOT landed this loop (recorded in the M0134-0053 ledger row, available for
a future standalone slice if picked up): (3) nested LIST/RANGE overlap
false-positive in `internal/executor/operators_ddl_partition.go`
(`validateListOverlap`/`validateRangeOverlap`/`validateHashBounds` read a
contaminated `PartitionBounds` field on multi-level sub-partitioned
tables — same root-cause family as the already-fixed M0134-0013b
`validateDefaultPartition`, needs the identical live-children-filter
rewrite); (5) custom multi-char operator `===` fails to lex (2
occurrences, low priority, not investigated further).

Next step: select **M0134-0054 (plancache.sql)** per the fix_plan
task-ID-ascending selection rule. Size it via `scripts/pg-regress-runner.sh
--verbose plancache` (delegate to researcher) before deciding
fix/split/park, same pattern as M0134-0044..0053. NOTE: partition_prune's
bucket-(3) nested-overlap fix and the growing "partition bound reasoning in
the optimizer" gap (now 2 parked tests pointing at it: M0134-0052 +
M0134-0053) may warrant a design-doc scoping pass at some point — not yet
done, flagged for future consideration, not blocking M0134-0054 selection.

Gates run this loop: `go build ./...` PASS; `go test ./internal/executor/
-run 'TestHashPartitionMultiColumnRouting|TestHashPartitionSingleColumn
RoutingRegression'` PASS; `go test ./internal/executor/` (full package)
PASS; `make ralph-state-guard` ran clean after one auto-repair
(status/progress reconciliation, recurring, not new); pre-commit pgbench
smoke PASS (374/696/12838 TPS across the 3 builtin scripts, no failed
transactions).

Delegation: researcher agent (1 round, sizing, found 6 buckets + PG oracle
citations, recommended PARK+PARTIAL — accepted as-is). implementer agent
`a0cba4a4a734cd4d6` (1 round, landed the HASH multi-column fix cleanly per
brief, DONE — no follow-up round needed; note: this agent's Write tool was
blocked from creating report.md by harness policy, so its findings were
relayed inline in the tool-result text instead of a file under
tmp/ralph-handoffs/ — folded directly into the ledger/fix_plan write-up
above, no durable-artifact loss).

In-flight: none. Commit `8312478e` pushed to `regress-renumbering`. No
server left running (regress runner + pgbench smoke both self-start/stop
their own throwaway goopg instances via the cgroup wrapper).
