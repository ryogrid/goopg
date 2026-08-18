# Working set — M0134-0005s landed (CREATE TABLE … INHERITS conislocal)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005s LANDED**. Parent case stays
`[ ]`. Selected per the Current Priority banner (M0134 after M-NIGHTLY). M-NIGHTLY
drained: `ci/logs/action-items.md` still at run `20260818-005518`, **items: 0**.

**What landed (design §26):** `CREATE TABLE … INHERITS` now marks a NOT NULL
`conislocal` whenever the child's own body supplies a local source (explicit
`NOT NULL`, table-level `CONSTRAINT n NOT NULL c`, or **PK-implied**), naming it
after the CHILD unless an explicit name was given — PG's `heap.c:3038-3050`
(`islocal=true` unconditionally + `ChooseConstraintName` over the child) vs
`heap.c:3057-3120` (parent's name, `islocal=false`, only when there is no local
source at all). Two edits in `operators_ddl.go`: capture `childAutoName` at `:3997`
before the `col.Inherited` branch overwrites `name`; set `isLocal = true` + name
fallback in the `len(entries) > 0` block (`:4056-4077`). `inhCount` untouched.
Measured **731 → 707 lines, hunks 32 → 31**.

**Three things worth not re-learning:**
- **A fully-cleaned hunk is worth its context lines.** Predicted 4-line payoff,
  got 24 — once `notnull_tbl4_cld2`/`cld3` matched byte-for-byte the whole hunk
  vanished. Marker-line counting systematically UNDER-predicts slice payoff on this
  file; the previous loop's over-prediction was the opposite error from a different
  cause (three-way cluster split).
- **`scripts/pg-regress-runner.sh` has its OWN bash `normalise_output`** (script
  lines 250-266) — it is NOT `NormalizeRegressOutput` in
  `internal/testport/framework/regress.go`. The framework one strips `LINE `/`^`,
  the gate one does not. A ledger row was corrected this loop for citing the wrong
  one; check which normaliser a claim is about before trusting a payoff estimate.
- **The twins were already correct.** `mergeNotNullOnAttach`/`unmergeNotNullOnDetach`
  got this right in 0005q/0005r, and the deferred-to-COMMIT INHERIT path shares the
  same call site — only the CREATE-TABLE-time path had the gap. Verified, not edited.

**Gates run:** `go build ./...`; `go test ./internal/{executor,catalog}/`;
`go test -run 'TestPort_.*(NotNull|Constraint|Inherit|Partition)' ./internal/testport/`
(75.5s PASS); `scripts/pg-regress-runner.sh constraints` (707/31);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (`internal/initdb`
ran cold at 438s — cache invalidation from the diff, not a regression);
`scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35** — Rule #1); pgbench smoke via hook.
Note: `SCOPE=units` EXCLUDES `internal/testport`.

**Next step — pick from the remaining cluster.** Baseline is now **707 lines / 31
hunks** (never compare to a pre-2026-08-18 number). Ranked:
1. **ATACC3 / `NO INHERIT` parent counted toward the child's `coninhcount`** —
   root cause pinned this loop at `operators_ddl.go:4012-4023` (skip a parent
   NOT-NULL whose `NoInherit` is set, as `MergeAttributes` does); the adjacent
   `col.NotNull` clearing at `:1854-1861` is the CORRECT half — do not touch it.
   ~4 lines, ledgered, no research needed. **Cheapest real bug left.**
2. The `ee.Pos` / `LINE N:` audit — do all ~20 `ee.Pos == 0` sites at once (36
   lines), not the two-site patch (6 lines); same review cost.
3. `ATExecValidateConstraint` not recursing to descendants (`tablecmds.c:13213-13290`).
4. The CHECK half of `MergeConstraintsIntoExisting` (matched by **name**, not attnum).
5. `DROP CONSTRAINT … ONLY` not decrementing the child's `InhCount`/`IsLocal`
   (`notnull_tbl5_child`, diff `:486-487`).
6. Cosmetic: missing "merging column" NOTICE; suppressed inheritance NOTICEs;
   `regclass` ORDER BY sorting by non-OID.
Items 3-6 still need their own researcher pass per §21.1; item 1 does not.

**Delegation:** `tmp/ralph-handoffs/m0134-0005s-census/` — researcher
`af4606fb7e2baade0` (1 round, DONE — two-candidate ranking).
`tmp/ralph-handoffs/m0134-0005s-createinh/` — implementer `aa9faa0ee2896eb70`
(1 round, DONE), tester `a89f23ef5418db786` (units + spotcheck PASS).
All complete; nothing to resume.

**In-flight:** none
