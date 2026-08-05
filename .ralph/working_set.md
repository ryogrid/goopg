(idle — nothing in flight)

Last loop: **M0127-P5.9-o CLOSED** (09 §3.17) — EXPLAIN prints a join's
RESIDUAL qual as PG's `Join Filter:` line. Before this, `ON jl.a = jr.a AND
jl.v < jr.w` showed the key as `Hash Cond:` and the second conjunct **nowhere
in the plan text**, on either arm — the same blind spot `Hash Cond:` closed at
P2.1, stopping one conjunct short.

`formatJoinFilter` (`internal/executor/operators_explain.go`) emits it in
upstream's slot (after `Hash Cond:`/`Merge Cond:`, before the node's own
`Filter:` — `ExplainNode`'s order for all three join arms) and gets the split
from `ExecHashKeyPlan`/`ExecMergeKeyPlan`, **the same methods the executor
uses** to decide what it re-checks per match (upstream's own
`list_difference(joinclauses, hashclauses)`). Consequence worth keeping: every
conjunct prints exactly once — in the Cond line if a key enforces it, as
`Join Filter` otherwise. Nested loop has no key list ⇒ whole Predicate.

Verified byte-for-byte against a throwaway PostgreSQL 18.3 cluster
(`initdb` → :5533, removed afterwards) on four shapes: one residual conjunct;
two, as `((jl.v < jr.w) AND (jl.b <> jr.b))`; all-equijoin two-key, where PG
prints **no** line; merge join.

`TestExplainQualifiesUpperFilter` is unpinned back onto the DEFAULT enumerator
(the pin existed only because the cross-relation residual — the one shape a
searched arm leaves at the join node — had no line). New:
`TestExplainRendersJoinFilterResidual`,
`TestExplainNoJoinFilterWhenKeysCoverThePredicate`.

**Read the next plan diff with this in mind:** every captured plan whose join
carries a non-key conjunct grows one line, so the SF0.5 **plan channel** will
report those queries `changed` with no planner change behind it; `make
plan-diff` (vs PG) should move the other way. 1 ledger row, 3 deferrals.

NEXT LOOP (subject to the fix_plan `## Current Priority` banner, which wins):
M0127-P5.9 successors — **-m** (collapse-ON acceptance pass, gates the COLLAPSE
flip; it runs the ~28 min SF0.5 sweep two loops have now deferred to it) and
**-p** (searched-arm batch-growth fixture).

Nightly triage: `ci/logs/action-items.md` is still run 20260806-011323, already
filed as an M-NIGHTLY harness item. Nothing new.

Gates run: `go build ./...`; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (no FAIL lines);
`scripts/tpch-spotcheck.sh` **PASS** (Q12=2, Q13=35, 28.3 s query phase);
pgbench smoke via the commit hook; `make ralph-state-guard`.

In-flight: none.
