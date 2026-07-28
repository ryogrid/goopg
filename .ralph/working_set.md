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

**NEXT LOOP — remaining M-NIGHTLY items (they still preempt M0124/M0125):**
- (e) two left: `regress/portals_p2` and `regress/select`. **Try the ISOLATED
  run first** — `go test -v -run 'TestPort_RegressSuite/^portals_p2$'
  ./internal/testport/` (~2 s). A `SKIP` there means "output mismatch", not "not
  applicable"; that is how root-0036 was found and it is much cheaper than the
  670 s prefix. The earlier "these only diverge in full-suite ordering" note is
  NOT true of every case.
  `/tmp/rdiff-loop6/portals_p2_{expected,actual}.txt` survived: PG returns 1 row
  per FETCH where goopg returns 2 (~10 blocks, plus one 3-row block) — smells
  like a single cursor-positioning bug, not ten.
- (d) the regress harness emits a phantom `deferred:` per case after a failed
  cluster restart (root-0032 §5 leftover).
- Then M0124 → M0125 per the 2026-07-28 directive.

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
