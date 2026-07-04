(idle — nothing in flight)

M0122-0003 (EXPLAIN FORMAT XML/YAML + per-CTE ANALYZE stats) committed and
pushed to `align-data-structure-with-pg` (`abb4a549`):
- FORMAT XML/YAML: `internal/parser/ast.go` gains `ExplainFormatXML`/
  `ExplainFormatYAML`; new `internal/executor/operators_explain_format.go`
  walks the existing `planToJSON`/`planToJSONWithStats` tree (no new
  tree-build logic), mirroring PG's `explain_format.c` tag sanitization /
  YAML line-starting rules. Tests: `explain_format_xml_yaml_test.go`.
- Per-CTE ANALYZE stats: `Build()`'s `CTEScan`/`CTEDMLPrefix`/
  `MaterializedCTEScan` cases now route through `maybeInstrument`, so the
  CTE node's own EXPLAIN line reports actual rows/time (previously only
  its inlined child did). Tests: `with_explain_test.go`. Residual deferred
  (ledger): `cteDMLPrefixOp.Open()` Builds its DML/body plans lazily,
  outside the ANALYZE instrumentation scope, so nested nodes under "CTE
  DML" still show cost-only estimates.
- Both slices were produced by two concurrently-running `ralph_loop.sh`
  iterations on the same shared working tree (no worktree isolation) and
  reconciled into one commit — verified disjoint + green before folding
  in, per the root-0026 precedent. Design:
  `docs/design/0122-0003-explain-format-xml-yaml.md`. 4 ledger rows added
  (SETTINGS/BUFFERS rendering, `pg_stat_io` real data, `track_io_timing`
  runtime SET, CTEDMLPrefix residual above).
- Gates: build/vet clean; `internal/executor` + `internal/parser` full
  packages PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench
  smoke via pre-commit hook PASS.

**Concurrency hazard — STILL UNRESOLVED after 3+ loops (#6, #7, #8).** Two
independent `ralph_loop.sh --live` trees are both running on this exact
working tree right now (named `ralph` screen PID 2085426; unnamed Attached
screen `2087325.pts-9`) — confirmed via distinct cwd/ancestry, not a
subshell false-positive. This loop hit a concrete collision (not just
theoretical git-merge evidence like #6/#7): found the sibling's uncommitted
CTE-instrumentation fix mid-task and reconciled it cleanly. Sent a
PushNotification to the human this loop per the #7 note's escalation
instruction (desktop notification; mobile not sent — Remote Control
inactive). Did not kill either process (Attached screen = possible live
human terminal; can't get sign-off autonomously). If loop #9 still finds
two trees, escalate again — a human should actively decide whether to
consolidate (`screen -r 2087325` or `-r 2085426` to inspect).

Next step: pick up the M0122-0003 remainder (SETTINGS/BUFFERS rendering +
`pg_stat_io` share a root cause — no buffer-pool hit/read counters exist
yet in `internal/executor/instrument.go`'s `nodeStats`; bundle as one unit
per the ledger rows) or continue the M0119-0004 pg_dump catalog-view parity
battery per `.ralph/fix_plan.md`.
