# Working set — M0134-0005 Bucket 6 landed; case still open, next target re-ranked

**Task:** M0134-0005 (`constraints.sql`) — **Bucket 6 LANDED 2026-08-18**; the case
stays `[ ]`. Selected per the Current Priority banner (M0134 next after M-NIGHTLY).
M-NIGHTLY drained: `ci/logs/action-items.md` still at run `20260818-005518`,
**items: 0** — nothing to file.

**The bucket's premise was wrong — don't re-derive it.** `ALTER TABLE … ALTER
CONSTRAINT` was NEVER a blanket parse error: `[NOT] DEFERRABLE` / `[NOT] ENFORCED`
already parsed (`parseAlterConstraintAttrs`, `internal/parser/ddl.go:2925`) and
`execAlterTableAlterConstraint` (`operators_ddl.go:10996`) carried real FK logic.
Only two spellings were broken, needing different work: **`NOT VALID` is
parser-only** (PG raises it in the *grammar action*, `gram.y:2672-2676`, `0A000`
"constraints cannot be altered to be NOT VALID") and **`[NO] INHERIT` needed both
arms**, copying a grammar asymmetry — bare `INHERIT` is its own production
(`gram.y:2686-2699`), `NO INHERIT` rides `ConstraintAttributeSpec` (`gram.y:6249`).
Executor arm mirrors `tablecmds.c:12615-12684` (contype-gated to `CONSTRAINT_NOTNULL`,
42704 unknown name, no-op if already in state, **one-level** child propagation).
Rule-#2 sibling check came back **negative** this time (unlike Bucket 3).

**Measurement — the real finding.** `constraints` diff 1431 → **1411** lines, hunks
**33 → 33**. Neither shrink nor unmasking but **bucket interference**: both target
statements now behave, yet every hunk holding them stays open on an unrelated
defect. Two capping defects isolated and ledgered: (a) `ADD CONSTRAINT … UNIQUE (i)
DEFERRABLE INITIALLY DEFERRED` builds the index immediately → `unique_tbl_i_key`
never exists (**Bucket 4's** defect — this also *invalidates the Bucket 2 ledger
row's* claim that those hunks awaited UNIQUE enforceability); (b) every `EXECUTE
get_nnconstraint_info(…)` returns `(0 rows)` file-wide. Also surfaced: `ALTER TABLE
… INHERIT` raises "would be inherited from more than once" where PG succeeds —
confirmed NOT caused by this diff (it touches no inheritance-attach code).
**Never compare to a pre-2026-08-18 `constraints` number** (pre-C19 harness).

**Files:** `internal/parser/ddl.go` (dispatch `:8892-8931`, `parseAlterConstraintAttrs`),
`internal/parser/ast.go` (`AlterConstraintNoInherit` / `AlterConstraintHasInheritability`),
`internal/parser/alter_constraint_test.go`, `internal/executor/operators_ddl.go`
(`execAlterConstraintInheritability`, `resyncNotNullCatalogHeap`),
`internal/executor/operators_ddl_alter_constraint_inherit_test.go`,
`docs/design/0134-0005-constraints-sql-divergence.md` (§6 Bucket 6, §7 next),
`docs/design/README.md`, `.ralph/fix_plan.md`, `.ralph/deferral_ledger.md` (2 rows).

**Next step:** stay on M0134-0005 and brief a **research pass on the
`get_nnconstraint_info` → `(0 rows)` masking bug** — cheapest remaining and it
unblocks the observability of several buckets at once (probe: run that PREPARE's
`SELECT` body standalone against goopg; suspect `pg_constraint` contype='n' row
population). Fallback: Bucket 3's NOT NULL inheritance-propagation gap
(`operators_ddl.go:4748-4753`). Bucket 4 is the highest *value* but is
milestone-sized (deferred UNIQUE checking) — research first, do not brief directly.
**Do not brief Bucket 7** (root cause unpinned). Bucket 5 (GiST `circle_ops`) is a
milestone.

**Gates run:** guard tests FAIL pre / PASS post (`TestAlterConstraintNotValidRejected`,
`TestParseAlterConstraintInherit`, `TestAlterConstraintInheritabilityToggle` — coordinator
re-ran all three post-handoff); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (~9 min, warm cache); `scripts/tpch-spotcheck.sh`
PASS (Q12=2, Q13=35 — executor change, Rule #1); `scripts/pg-regress-runner.sh
constraints` re-measured 1411/33. No TPC-DS — DDL-only change.

**Delegation:** `tmp/ralph-handoffs/m0134-0005-s05-alter-constraint-attrs/`
(researcher `a9b46057165f682fd` 1 round → corrected the bucket premise; implementer
`a7363c249ae5ee8c5` 1 round DONE; tester `ae4b501789c6f0bdb` 1 round, 3 gates).

**In-flight:** none.
