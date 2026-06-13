# Working Set (carried from loop 5, 2026-06-13)

## Done this loop — RESTORED BUILDABLE HEAD (M0100-0005 blocker #1 cleared)

Commit `c0e4842f` "fix(executor): commit missing CTE self-modification Context
fields (restore buildable HEAD)".

**Root cause (corrected from loop 4's wrong hypothesis):** NOT concurrent-loop
contamination. ppid analysis: the two `ralph_loop.sh --live` procs are ONE loop
(pid 2214963.ppid == 2113421 = the portable_timeout subshell; see memory
`concurrent_ralph_loops_corrupt_tree`). The break was a chronic split-brain from
`29de7a95` (M0100-0010): it committed consumer refs to `ctx.CTENewToOld` /
`CTESelfModifiedErrors` / `CTESelfModErr` in operators_storage.go, but the field
declarations in context.go were never committed. Every later loop ran `go build`
with the uncommitted context.go present → passed locally, masking the break.

**Fix:** committed only the 2 coherent files — context.go (declare 3 fields) +
operators_cte_dml.go (init 2 maps). Verified pure-HEAD `go build ./...` fails
ONLY on those 3 fields; with them, builds standalone. gofmt+vet clean.

## Still uncommitted (DO NOT bundle — separate WIP, ~771 lines, 21 files)
gen_override (parser/ddl, ast, catalog, executor + 2 untracked test files —
`TestPartitionChildGeneratedExprOverride` currently FAILS, impl incomplete),
lockrows (+393), planner (bushy/nl_index_join/unnest/plan/planner), join_agg,
analyzer, subxact_visibility, dispatch, opnode, operators.go. None referenced by
committed code; owned by other in-flight tasks. Backup: /tmp/loop5-backup/.

## Gates run this loop
- pure-HEAD `go build ./...` (WIP stashed, untracked tests moved out): PASS
- gofmt -l (committed files): clean ; go vet ./internal/executor/: clean
- go test ./internal/executor/ : only FAIL is gen_override test (impl stashed) —
  not part of this commit
- make ralph-state-guard: (run at loop end)

## Next step (M0100-0005 remaining, next loop)
Re-run 22 `TestPort_Isolation*` on clean HEAD; verify pgbench-S TPS≥2000 (needs
data dir — see MAINT-TPCH-RELOAD); reconcile/strike `Depends: Close of M0107`
against the 0100 milestone-doc DoD; flip statuses in
postgres-oracle-port-status.csv (D-002), mark M0096-0005/M0096-0013 `[x]`, set
milestone 0100 + README `accepted`.
