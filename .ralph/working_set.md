(idle — nothing in flight)

Loop #3 landed and committed the `CTEDMLPrefix` nested-node EXPLAIN
instrumentation residual (M0122-0003), closing deferral ledger rows
467/468, at commit `89753fee`. Fix: new `instrumentScopeCarrier`
interface (`internal/executor/instrument.go`) lets `maybeInstrument`
hand `cteDMLPrefixOp` (`internal/executor/operators_cte_dml.go`) the
`*instrumenter` active on its own `Build()` call; its two lazy
`Build()` sites (DML plans + outer body, only Built inside `Open()`
after CTE write-then-snapshot-restore ordering) now run through a new
`buildUnderScope` helper that reinstates it, so nested nodes (e.g.
`Insert on t`) register in the same `nodeStatsTable` the EXPLAIN
renderer already walks. New test:
`TestExplainCTEDMLPrefixNestedInsertReportsActualRows`.

Gates run: `go build ./...` clean; `go vet ./internal/executor/...`
clean; `go test -count=1 ./internal/executor/... ./internal/storage/...
./internal/planner/... ./internal/parser/... ./internal/server/...
./internal/config/...` all PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); pgbench pre-commit smoke PASS (auto-run by hook);
`make ralph-state-guard` (repaired stale completed-marker→in_progress,
same recurring pattern as prior loops, now consistent).

Docs updated same loop: `.ralph/fix_plan.md` (M0122-0003 item +
banner), `.ralph/deferral_ledger.md` (new resolved row closing
467/468), `docs/design/0122-0003-explain-format-xml-yaml.md` (new
"`CTEDMLPrefix` nested-node instrumentation" section) +
`docs/design/README.md` index row extended.

Next step: per fix_plan.md "Next up" banner, remaining M0122-0003
sub-items are `EXPLAIN (BUFFERS)` without ANALYZE (planning-time
buffers — no planning-phase buffer-counting mechanism exists),
local/temp-buffer terms, and the `reuses` `pg_stat_io` op counter
(needs a new `BufferAccessStrategy`-style ring-buffer mechanism in
`internal/storage/bufpool.go` — feature-sized, likely needs its own
decomposition). Alternatively continue the M0119-0004 pg_dump
catalog-view parity battery / next unresolved DU-002 slice from
`.ralph/deferral_ledger.md`. Recommend `EXPLAIN (BUFFERS)` without
ANALYZE next — smallest, most self-contained of the remaining options.
