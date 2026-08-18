# Working set — M0134-0005i landed (NOT-NULL inheritance cascade, −42 lines)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005i LANDED**. Sub-item `[x]`,
parent case stays `[ ]`. Selected per the Current Priority banner (M0134 after
M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` still at run
`20260818-005518`, **items: 0** — nothing to file.

**What landed:** `ALTER TABLE … SET NOT NULL` and `ADD CONSTRAINT … NOT NULL` never
cascaded to a parent's **already-existing** children — both handlers
(`operators_ddl.go` ~:9661 / ~:9754) mutated only the table named in the ALTER, so a
child silently kept accepting NULLs and every later `VALIDATE`/`DROP`/`COMMENT ON
CONSTRAINT <parent-name> ON <child>` failed "does not exist". One shared
`cascadeNotNullToChildren` (+ `maxNotNullCascadeDepth = 64`) now serves both.

**Four things worth not re-learning:**
- **The slice was chosen from a MEASURED bucket census, not a guess** — and that is
  why it worked. The baton's own second hypothesis (`COMMENT ON CONSTRAINT`) was
  **refuted**: a downstream symptom of the missing cascade, not an independent bug.
  Regenerate with `GOOPG_CG_UNIT=<n> scripts/pg-regress-runner.sh --verbose constraints`.
- **Unwired, not missing — the FOURTH time in this milestone.** The DROP-CONSTRAINT
  sibling already cascades; `CREATE TABLE … INHERITS` already does the merge
  accounting. Probe the producer before any "needs new infrastructure" claim.
- **Recursive, not one-level.** PG's `ATExecSetNotNull` re-invokes itself per child
  (`tablecmds.c:8062-8079`), so grandchildren are reached. The DROP sibling's
  one-level walk is a ledgered defect class — do not copy it verbatim.
- **Pre-fix failures were silent SUCCESSES.** No message-diff guard would ever have
  caught this; the merge and negative guards were mandatory, not optional.

**Measured: `constraints` 1164 → 1122 lines (−42), hunks 33 → 33**, as a true
stash-based counterfactual (post-fix, stash production file only, re-measure,
restore). A single post-fix reading is not attribution.

**Next step — the successor slice is bucket 2, and it is a SILENT WRONG ANSWER:**
`DEFAULT -1 * currval('insert_seq')` evaluates to NULL on goopg, which both corrupts
`SELECT` output and lets `CHECK (x + z = 0)` accept violating rows (a NULL operand
makes the predicate NULL, and PG rejects only FALSE — `execMain.c:ExecConstraints`).
~150–180 lines across `INSERT_TBL`/`INSERT_CHILD`/`COPY_TBL`. It is a
default-expression-evaluation bug, NOT constraint propagation. Ranked buckets 3–8 are
in design doc §16.1 with line counts. **Bucket 7 (grammar) and bucket 8 (float8
formatting) are tiny; bucket 3 (GiST `circle_ops`) is a known pre-existing gap.**
**Probe empirically before briefing** — this doc's guesses have now been wrong six
times and right once (this loop) when measured first.

**Gates run:** `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(7m46s; `internal/initdb` 459s / `cmd/goopg` 79s fresh — input change, not a cold
cache); `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35 — Rule #1);
`go build ./...`; `go test ./internal/executor/ ./internal/catalog/` PASS;
`go test -run 'TestPort_.*(NotNull|Inherit)' ./internal/testport/` PASS (7 subtests);
3 positive guards proven FAIL-pre by stashing only `operators_ddl.go`; pre-commit
pgbench smoke PASS via the hook.

**Delegation:** `tmp/ralph-handoffs/m0134-0005i-probe/` (researcher `ac283f8f2e5510867`,
DONE — bucket census, refuted hypothesis B, pinned the cause);
`tmp/ralph-handoffs/m0134-0005i-notnull-inherit-cascade/` (implementer
`a4df7500b1a046454`, 1 round, DONE); `tmp/ralph-handoffs/m0134-0005i-gates/`
(tester `ad0c1cab7d1268b14`, DONE).

**In-flight:** none.
