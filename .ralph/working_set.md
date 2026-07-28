(idle — nothing in flight)

Last loop (#8): landed **root-0036** — M-NIGHTLY item (e)'s `regress/select_distinct`
is green. Committed + pushed.

The whole divergence was one 20-row block returned reversed. `USING >` and the
`person*` inheritance scan in the failing query are BOTH red herrings, and so is
the harness's "normalization rules need extension" wording. Reduced:
`SELECT DISTINCT p.age FROM person p ORDER BY age DESC` answered **ascending**;
the unqualified `SELECT DISTINCT age FROM person ORDER BY age DESC` was fine.
goopg dedups via a fixed ascending sort in `distinctOp` and re-applies ORDER BY
with an outer Sort (M0097-0046) — that Sort was the sole carrier of direction and
was silently dropped whenever its key failed to resolve. It usually failed:
`resolveOrderBySubstitution` rewrites a bare ORDER BY name into the target's OWN
(qualified) expression, while the outer ctx is schema-only with no bindings and
`SchemaColumn` carries no table name. 7 of 8 measured shapes were wrong. Fix adds
a positional arm (star targets) + `distinctSortKeyOutputIndex` (resolve against
the surface that built the targets, match `proj.Targets` by `exprEqual`).

**NEXT LOOP — M0124-0001, chunk 1 (2026-07-28(b) re-prioritisation):**
- Run `scripts/tpcds-bench-compare.sh 1-8` (foreground, Bash `timeout` 55 min,
  `ENGINES="goopg pg" TIMEOUT_SEC=600 RESTART_AFTER_TIMEOUT=1`), stdout to
  `analysis/tpcds-sf1-resweep-20260728/chunk-1-8.txt`. Then carry the cursor
  (`M0124-0001 sweep: 1-8 done; next 9-16`) here. Full protocol: the "Chunked
  execution" note on the M0124-0001 task in fix_plan.md + design doc
  `docs/design/0124-0001-tpcds-sf1-head-resweep-protocol.md`.
- **M-NIGHTLY is PARKED** (banner amendment 2026-07-28(b)): keep FILING new
  `AI-` items each loop, do not select them. The two remaining regress cases
  (`portals_p2`, `select`) and the phantom-`deferred:` harness item (d) keep
  their resume notes on the parked task in fix_plan.md — do NOT start them.
- No engine commit may land until the sweep completes: every chunk header prints
  `# goopg: <git log -1>` and they must all name the same SHA.

**Standing gotchas:** `RALPH_PRECOMMIT_SCOPE=units` does NOT cover
`internal/server` — run it explicitly. Neither `tpch-spotcheck.sh` nor the TPC-DS
SF0.5 gate can see a wrong-order/right-rows bug (both are row-count only) — new
ledger row 2026-07-28. TPC-DS Q54 reliably kills the capped sf05 server (matches
its recorded TIMEOUT baseline); don't read that as a regression.

Gates run: new non-vacuous `TestDistinctHonoursOrderByDirection` (7/10 subtests
red with the planner hunk stashed); `TestPort_RegressSuite/select_distinct`
SKIP→PASS (negative control confirmed SKIP before the fix); `go test
./internal/planner/ ./internal/executor/ ./internal/server/` PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); TPC-DS Q41/Q6/Q87 on sf05 match
oracle + 2026-07-27 baseline; pgbench smoke via the commit hook.
In-flight: none.
