# Working set — M0134-0005f landed; 0005g filed (named SET CONSTRAINTS)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005f LANDED**. Sub-item `[x]`,
parent case stays `[ ]`. Selected per the Current Priority banner (M0134 next after
M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` still at run
`20260818-005518`, **items: 0** — nothing to file.

**What landed:** per-partition UNIQUE/PK index clones dropped the parent's
`Deferrable`/`InitiallyDeferred`/`IsConstraint`. Two symptoms, one cause: `pg_constraint`
showed 1 row where PG shows 3, and a deferred duplicate INSERT errored immediately
instead of at COMMIT. Both clone sites now forward the three fields via the
pre-existing `btreeIndexProps` struct: `internal/executor/operators_ddl.go:4611`
(`PARTITION OF`) and `:8380` (`ATTACH PARTITION`). Downstream consumers
(`uniqueCheckDeferred`, `PGConstraintRowsForDBOid`) were already correct and untouched.

**Three things worth not re-learning:**
- **PG cannot have this bug by construction** — `indexcmds.c:DefineIndex` recurses on
  the *same* `IndexStmt*` per partition (`indexcmds.c:706`). goopg clones a fresh
  `catalog.Index` field-by-field, so every clone site is a silent-drop candidate.
  Ledgered as an unaudited architectural class.
- **The implementer's escalation was correct and load-bearing** — the brief's literal
  probe SQL used the *named* `SET CONSTRAINTS <name>`, which is a DIFFERENT bug. It
  refused to absorb it; I split it out as 0005g. Guard was retargeted to the unnamed
  `ALL` form and re-proven FAIL-pre, not softened.
- The surviving `parted_uniq` hunk is **not** a psql echo artifact (the tester's first
  read) — goopg's ERROR precedes `COMMIT;` because it errors at the INSERT. That is 0005g.

**Measured:** 1181 → **1164** lines (−17), hunks 33 → **33**.
Artifact `tmp/regress-diffs/constraints.diff`. **Never compare to a pre-2026-08-18 number.**

**Next step:** re-measure first, then pick. Candidates, best first: **M0134-0005g**
(named `SET CONSTRAINTS` → `conparentid`-equivalent linkage; needs a new
`catalog.Index` field, size >1 slice, and **probe the FK twin first** — the same flat
resolver serves `FKConstraintDeferred`). Otherwise §11.4's remaining list: CHECK-constraint
inheritance naming, COPY FROM not rejecting bad rows, Bucket 3's NOT NULL inheritance
leftover, Bucket 5 (GiST `circle_ops`, a genuine milestone). **Bucket 7 still has no
pinned root cause — do not brief from it.** **Probe empirically before briefing** —
this doc's own guesses have been wrong four times.

**Gates run:** `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(~7m52s; `internal/initdb` 417s uncached — input change, not a cold-cache event);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35 — Rule #1); `internal/executor` +
`internal/catalog` + `internal/postmaster` PASS; all 3 new guards PASS with FAIL-pre
proven by stashing only `operators_ddl.go`; pre-commit pgbench smoke PASS via the hook.

**Delegation:** `tmp/ralph-handoffs/m0134-0005f-probe/` (researcher `af58d10808a28c43a`,
DONE — live two-engine probe pinned the cause);
`tmp/ralph-handoffs/m0134-0005f-s1-partition-index-constraint-attrs/` (implementer
`a688b010429ee11f0`, 2 rounds, DONE — round 1 NEEDS-DECISION, round 2 retarget);
tester `a354a0fb1b0ebaeac` (gates + re-measure).

**In-flight:** none.
