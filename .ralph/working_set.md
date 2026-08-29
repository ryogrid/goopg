(idle — nothing in flight)

## Loop #17 result — M0134-0170 RESUMED from a predecessor's orphaned WIP, landed, case PARKED

**The baton lied, and the tree told the truth.** `working_set.md` said `(idle)`, but
`git status` showed engine edits with mtimes 10:00/10:26 — minutes old — and their
comments cited **M0134-0170**, my own next task. A predecessor loop had been cut off
mid-task without rewriting the baton (`.ralph/progress.json` flipped to `completed`
at 09:47, edits continued to 10:26, and it left `tmp/wt-0170` + `tmp/ab-head`
worktrees behind). `ps` confirmed only ONE ralph loop alive (mine), so this was
orphaned WIP, not a live peer. **Always diff the tree against the baton's claim
before treating "idle" as authority — check mtimes, and grep the diff for a task id.**
I nearly burned the loop re-implementing it in a fresh worktree.

**Nightly triage:** `ci/logs/action-items.md` still run `20260828-235424`, both `## AI-`
items already filed (001 checked/fixed, 002 open duplicate). Nothing new.

**Task:** M0134-0170 `sqljson_queryfuncs.sql` sized live (`not-tried` → **`failed`**,
**2021 diff lines / 259 `^+ERROR`**), 100% SQL/JSON query-function family → **PARKED**
on ledger 0168a (third case in a row: -0168, -0169, -0170).

**Shipped (engine-wide):** index expressions and partial-index predicates are now
gated on IMMUTABILITY (42P17), and the partition-key sibling gained the built-in half
it never had. goopg had ONE of upstream's three ports of that predicate. Design
`docs/design/m0134-0170-index-expression-mutability.md`. Also two `pg-regress-runner.sh`
guards (busy auto-start port; `psql` exit-2) — this case had first been "sized" at a
**fabricated 1291 lines** because a foreign server on 15435 answered the readiness probe.

**Three things worth carrying:**

1. **A harness that cannot fail loudly reports a plausible number instead** — and that
   number gets written into fix_plan/the ledger as fact. Both failure modes are now fatal.
2. **Same line count ≠ same content, again.** 14-case A/B: identical counts everywhere,
   but `create_index` differed — a Go pointer address leaking through `pg_get_indexdef`
   (`&{105 0x… C}`), which differs between ANY two runs at HEAD too. Ledgered 0170c.
3. **A by-name volatility approximation must be asymmetric.** The 31 mixed-volatility
   `pg_proc.dat` names are EXCLUDED: rejecting legal DDL is worse than admitting DDL PG
   rejects. Ledger 0170a.

Gates run: `go build ./...` OK; guard `index_mutability_test.go` PASS and
**revert-checked** (10 index subtests + partition test fail when reverted);
14-case DDL regress A/B vs `tmp/ab-head` (13 byte-identical, zero regressions);
case A/B byte-identical at 2021 (fix is invisible to it, as expected);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=34);
`make regen-testport` + `make check-testport-inventory` PASS.

In-flight: none.

**Carried obligations (15th loop):** TPC-DS SF0.5 gate still NOT run (for -0156,
-0157). -0158..-0170 are parser/DDL/catalog/ACL/wire/type-input-only and cannot move
a TPC-DS plan.

**NEXT LOOP:** banner rules — M-NIGHTLY filing, then M0134-0171 (`foreign_key.sql`,
`failed`). Scratch worktrees `tmp/ab-head` (HEAD A/B baseline, has the postgres
symlink) and `tmp/wt-0170` (unused) are reusable; remove `tmp/wt-0170` when convenient.
