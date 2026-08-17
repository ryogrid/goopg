# Working set — M0134-0005 Bucket 4 core fix landed; next is the exposed index-build defect

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005b landed and pushed**
(`7ac8c177`). Sub-item `[x]`, parent case stays `[ ]`. Selected per the Current
Priority banner (M0134 next after M-NIGHTLY). M-NIGHTLY drained:
`ci/logs/action-items.md` still at run `20260818-005518`, **items: 0** — nothing to
file.

**What landed:** goopg had TWO unique check-timing tiers where PG has THREE. PG sets
`pg_index.indimmediate=false` for **every** deferrable index regardless of INITIALLY
mode (`catalog/index.c:2080-2082`); only the recheck *timing* differs.
`uniqueCheckDeferred` now answers only "needs a partial check?" (explicit-txn gate
**removed**); new `uniqueCheckDeferToCommit` holds the old resolver; queue entries
carry a `DeferToCommit` tier tag (plain-key **and** NND twin); new
`RunStmtEndDeferredUniqueChecks` drains the non-commit tier. Files:
`internal/executor/{deferred_unique.go,session.go}`,
`internal/postmaster/{dispatch.go,dispatch_extended.go}`,
`internal/testport/deferred_unique_stmt_end_e2e_test.go` (5 tests).

**Two things worth not re-deriving:**
1. **Bucket 4's "MILESTONE" sizing in §2 was wrong** — the machinery already existed
   (M0119-0004). Size buckets from the existing code, not the symptom.
2. **The dispatch twins were NOT symmetric.** Extended-protocol out-of-block
   `Execute` commits via `ectx.CommitTransaction` (`internal/executor/context.go:1030`),
   a bare `TxnMgr.Commit` with **no** deferred drain. UNIQUE is wired there now; **FK
   and EXCLUDE still are not** — silent uncaught violations for prepared-statement
   clients (ledgered, real bug).

**Measured:** 1376 → **1299** lines (−77), hunks 34 → **36** (unmasking split).
`timeout 300 scripts/pg-regress-runner.sh --verbose constraints`; artifact
`tmp/regress-diffs/constraints.diff`. **Never compare to a pre-2026-08-18 number.**

**Do not misread the raw diff** (§10.3): the "9 rows vs expected 5, no error" block is
a **cascade** of `unique_tbl_i_key` never being created, NOT a data-integrity
regression — with no constraint, those duplicates are legitimately unconstrained.

**Next step:** **M0134-0005c** — `ADD CONSTRAINT … UNIQUE (i) DEFERRABLE INITIALLY
DEFERRED` fails `Key (i)=(1) is duplicated` while the preceding `SELECT *` matches PG
byte-for-byte. **Hypothesis, UNCONFIRMED: the eager validate-then-build scan counts
dead row versions** (the `UPDATE i=i+1` now succeeds and leaves old versions behind —
exposed by, not caused by, 0005b). **Confirm the hypothesis with a targeted probe
before briefing an implementer** (UPDATE a row, then ADD CONSTRAINT UNIQUE on an
untouched column). Resume: `internal/executor/operators_ddl.go:11969` / `:12016` vs
PG's MVCC-aware `catalog/index.c:index_build` → `IndexBuildHeapScan`. It caps the rest
of Bucket 4. After that: research slices 3/4 (`UNIQUE ENFORCED` grammar rejection;
`ALTER CONSTRAINT … ENFORCED` contype gate), then Bucket 3's NOT NULL inheritance
leftover. ~20 of the 36 hunks are untouched by Bucket 4 — it is no longer the dominant
line driver. **Bucket 5 (GiST `circle_ops`) is a real milestone; do not brief Bucket 7.**

**Gates run:** 10-test guard 10/10 PASS (3 FAIL-pre/PASS-post via stash);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35 — Rule #1); deferred-FK cross-tier
PASS; pre-commit pgbench smoke PASS (12886 TPS). `make ralph-state-guard` OK after
self-repair.

**Delegation:** `tmp/ralph-handoffs/m0134-0005-b4-research/` (researcher
`a2dc55818f96adfc3`, DONE — the report is excellent, read it before 0005c);
`tmp/ralph-handoffs/m0134-0005-b4-s1-stmt-end-unique/` (implementer
`a6cd58aa69aad90b7`, 1 round DONE, no deviations — note its report.md was
coordinator-transcribed, its own write was tool-blocked); testers
`a534047298f10d48e` (gates), `a490e2899abc78fec` (re-measure).

**In-flight:** none.
