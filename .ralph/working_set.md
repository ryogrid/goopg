# Working set — M0134-0005d landed; constraints.sql has no cheap slices left

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005d landed**. Sub-item `[x]`,
parent case stays `[ ]`. Selected per the Current Priority banner (M0134 next after
M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` still at run
`20260818-005518`, **items: 0** — nothing to file.

**What landed (two line-disjoint parts, one implementer slice):**
1. **Parser.** PG accepts `ENFORCED` in the *grammar* (`gram.y:4135-4150`, ordinary
   `ConstraintAttributeElem`) and rejects it semantically in
   `parse_utilcmd.c:3991-4021` `transformConstraintAttrs` — `misplaced [NOT] ENFORCED
   clause`, **42601**, **with** a caret (under `ENFORCED` bare; under `NOT` for the
   pair). goopg emitted a generic syntax error because `parseConstraintDeferrable`
   (`internal/parser/ddl.go:2786`) unconditionally swallows a leading `NOT` while
   probing for `NOT DEFERRABLE`. New `rejectMisplacedEnforced` reuses the pre-existing
   `isEnforcedAttr` + the `SyntaxError{…,Raw:true}` template; calling it **before**
   `parseConstraintDeferrable` at the four column-level sites *is* the fix.
2. **Executor.** `ALTER CONSTRAINT … [NOT] ENFORCED` already matched PG's message and
   SQLSTATE (42809); only a spurious `LINE`/caret differed. Dropped `Pos: act.Pos()`
   from two `ExecError`s in `execAlterTableAlterConstraint` (`operators_ddl.go`
   ~:11061, ~:11091). `Pos == 0` ⇒ no wire `'P'` field.

**Two traps recorded so they are not re-hit:**
- **Table-level `UNIQUE (…) ENFORCED` is a DIFFERENT PG path** (`processCASbits`):
  message `"UNIQUE constraints cannot be marked ENFORCED"`, SQLSTATE **0A000**.
  Reusing the column-level text there would be a NEW divergence. Ledgered, not fixed.
- **Do not sweep the 4 sibling `Pos: act.Pos()` sites** in the same function — one
  (`constraints cannot be altered to be NOT VALID`) currently matches PG *with* a
  caret. Per-site verification only. Ledgered.

**Measured:** 1251 → **1232** lines (−19), hunks 35 → **35** (no split); every `+`/`-`
ENFORCED line gone (3 context lines remain). `timeout 300
scripts/pg-regress-runner.sh --verbose constraints`; artifact
`tmp/regress-diffs/constraints.diff`. **Never compare to a pre-2026-08-18 number.**

**Next step:** `constraints.sql` has **no cheap slices left** — §11.4 lists only
CHECK-constraint inheritance naming, COPY FROM not rejecting bad rows, Bucket 3's NOT
NULL inheritance leftover, the partitioned `parted_uniq_tbl` pair, and Bucket 5 (GiST
`circle_ops`, a genuine milestone). **Bucket 7 still has no pinned root cause — do not
brief from it.** Strong candidate instead: the ledgered **silent** integrity gap from
0005c — an explicit-txn UPDATE that PG rejects at COMMIT with a duplicate key
**silently commits** on goopg (2 of the 35 hunks). Otherwise move to the next M0134
case in CSV order. **Probe empirically before briefing** — the doc's own guess has now
been wrong three times.

**Gates run:** `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(initdb + cmd/goopg ran cold — cache miss, not a regression signal);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35 — Rule #1);
`go test ./internal/parser/... ./internal/executor/... ./internal/postmaster/...` PASS;
new guards FAIL-pre/PASS-post by stashing the single changed file.

**Delegation:** `tmp/ralph-handoffs/m0134-0005d-enforced-clause-fidelity/`
(researcher `a16a0752465c2409e`, DONE — the live probe is what found the `NOT`-swallow);
`tmp/ralph-handoffs/m0134-0005d-s1-enforced-fidelity/` (implementer
`aa5b5271315b7a841`, 1 round DONE, no deviations); tester `ad227756292443d23` (gates).

**In-flight:** none.
