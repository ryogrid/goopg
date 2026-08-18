# Working set — M0134-0005r landed (plain INHERIT/NO INHERIT NOT NULL merge)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005r LANDED**. Parent case stays
`[ ]`. Selected per the Current Priority banner (M0134 after M-NIGHTLY). M-NIGHTLY
drained: `ci/logs/action-items.md` still at run `20260818-005518`, **items: 0**.

**What landed (design §25):** `mergeNotNullOnAttach` gained an `isPartition bool`
gating PG's *only* `is_partition` delta — `conislocal` is cleared only for a
partitioned parent (`tablecmds.c:17771-17777`), so plain INHERITS leaves the child's
constraint both local *and* inherited. `unmergeNotNullOnDetach` needed **no** change
(`RemoveInheritance`'s decrement loop has no is-partition branch). Wired at four call
sites across both execution paths. Measured **738 → 731 lines, hunks 31 → 32**.

**Four things worth not re-learning:**
- **`ALTER TABLE … {NO} INHERIT` has TWO paths** (Rule #2 twins). The immediate case,
  and the deferred-to-COMMIT `ApplyPendingInheritanceChanges:7341` (M0118-0008
  transactional-DDL visibility). Round 1 wired only the first; the coordinator caught
  it in diff review. Any future inheritance-side change must touch both.
- **`RegisterInheritanceChild` must run AFTER the merge.** Otherwise a failed first
  INHERIT leaves the edge registered and the *correct* retry in `constraints.sql`
  trips a false 42710 "inherited from more than once" — mirrors PG's `CreateInheritance`
  order (dup check → column merge → constraint merge → `StoreCatalogInheritance1`).
- **The +1 hunk is an unmask, not a regression.** `ee.Pos = act.Pos()` stamping at the
  merge call sites emits a `LINE N:`/caret block PG doesn't; pre-existing at the
  ATTACH sites, newly visible at the INHERIT ones. Ledgered.
- Predicted 715-724/31, got 731/32 — the census's line attribution was optimistic
  because the cluster splits three ways. Partial shrink remains the right target.

**Gates run:** `go build ./...`; `go test ./internal/{executor,catalog}/`;
`go test -run 'TestPort_.*(NotNull|Constraint|Inherit|Partition)' ./internal/testport/`
(76s, PASS); `scripts/pg-regress-runner.sh constraints` (731/32);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (`internal/initdb`
ran cold at 441s — cache invalidation from the diff, not a regression);
`scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35** — Rule #1); pgbench smoke via hook.
Note: `SCOPE=units` EXCLUDES `internal/testport`, so the new tests are covered only by
the explicit `-run TestPort_` invocation above.

**Next step — re-measure, then pick from the remaining cluster.** Baseline is now
**731 lines / 32 hunks** (never compare to a pre-2026-08-18 number). Ranked:
1. **The `ee.Pos` / `LINE N:` annotation quirk** — cheapest clean filler (~4-6 lines,
   two call sites, no design work); ledgered this loop with the exact resume point.
2. Cluster B — `CREATE TABLE … INHERITS` merge-time NOT NULL name/`conislocal`
   (`operators_ddl.go:3966-4051`, ~26 lines). A real pre-existing bug, not a missing
   call: the witnessed case is a child column redeclared *without* its own NOT NULL.
3. `ATExecValidateConstraint` not recursing to descendants (`tablecmds.c:13213-13290`).
4. The CHECK half of `MergeConstraintsIntoExisting` (matched by **name**, not attnum).
5. Cosmetic: cluster C's missing "merging column" NOTICE; suppressed inheritance
   NOTICEs; `regclass` ORDER BY sorting by non-OID.
Each still needs its own researcher pass per §21.1.

**Delegation:** `tmp/ralph-handoffs/m0134-0005r-inheritnn/` — researcher
`ae2980a8307a3665a` (1 round, DONE — census + pinned oracle), implementer
`a323f0ee4bda45d42` (2 rounds, DONE), tester `a829ddc556967c662` (units + spotcheck
PASS). All complete; nothing to resume.

**In-flight:** none
