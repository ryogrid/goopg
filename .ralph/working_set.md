# Working set — M0134-0005ar LANDED (AddCheckInherited flag blindness, both halves)

**Task:** M0134-0005 / sub-item **M0134-0005ar** — LANDED, committed, pushed.
One researcher + one implementer + one tester round, zero escalations, one
non-blocking deviation. No resume needed.

**Selection:** M-NIGHTLY had nothing new — `ci/logs/action-items.md` still at run
20260819-011823, whose only `AI-…-001` is filed AND `[x]` fixed. M0134 next per
the banner; took the baton's ranked #1.

**What landed.** `catalog.Table.AddCheckInherited` (`catalog.go:305`) hard-zeroed
`NotValid`/`NotEnforced`, so every CHECK propagated to an inheritance or partition
child was born enforced and pre-validated. Widened to
`(name, expr, oid, notValid, notEnforced)`, matching sibling `AddCheckFull`
(`:295`) which had all three flags from the start. All three call sites fixed.
Closes ledger rows `:1540` (0005z, NotEnforced half) and `:1573` (0005an,
NotValid half), which had each deferred with "land them together." +35/−12.

**THE FINDING — both ledger rows predicted a SYMMETRIC fix, and PG is not.**
PG has two rules, split on whether the child existed when the constraint was
declared:
- CREATE-time (`INHERITS`, `PARTITION OF` → `MergeCheckConstraint`,
  `tablecmds.c:3219-3220`): `is_enforced = is_enforced; skip_validation =
  !is_enforced` — validity derives **purely from enforcement**, never from the
  parent's `convalidated`. A fresh child is empty ⇒ trivially valid. Upstream has
  no regress case for it because the answer is always *valid*.
- ALTER-time (`ATAddCheckNNConstraint`, `tablecmds.c:9912-10049`): the **same
  `Constraint` node** is reused per child ⇒ user's literal clause passes through,
  and Phase 3 queues per-child validation (`:9956-9966`) since children may
  already hold rows.
Sites: `operators_ddl.go:4049` + `:5175` get `(parent.NotEnforced, ×2)`; `:8765`
gets `(act.NotValid, act.CheckNotEnforced)`. An **anti-symmetry guard**
(`TestCheckInheritFlagsCreateTimeAntiSymmetryNotValid/{INHERITS,PARTITION_OF}`)
pins this so a future loop cannot "tidy" the three sites into one rule.

**Seventh consecutive Rule-#2 sighting.** `execCreatePartitionChild`'s comment at
`:4026` claims it mirrors the INHERITS loop; it did not — site 1 had 0005z's
post-hoc stamp, site 2 had nothing at all. Drifted for a whole slice.

**Deviation (non-blocking, now ledgered as BLOCKING for the fixture).** The
literal port of `alter_table.sql:397-406` passes **vacuously** both pre- and
post-fix: `AlterTableAddCheck`'s cascade walks `PartitionChildren` only, never
`InheritanceChildren`, so `attmp6`/`attmp7` never get a `NamedChecks` entry to get
wrong. Retained as a regression pin. That is the 0005z row's second clause
(`:1539`) — now the blocking prerequisite for the fixture, not a parallel gap.

**Metric:** `constraints.sql` enters and exits at **176 lines / 8 hunks**
(unchanged) — it exercises neither a NOT VALID nor a NOT ENFORCED inherited
CHECK. No per-slice diff number claimed, deliberately.

**Gates run:** 5/5 new guards PASS (3 FAIL-pre proven by stashing only the two
source files); `RALPH_PRECOMMIT_SCOPE=units` PASS (~7m, `internal/initdb` +
`cmd/goopg` cold — cache cold-start, not a regression);
**`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35)**; build + vet clean; pgbench
smoke via hook.

**Next step:** continue M0134-0005 at the **176/8** baseline. Ranked:
1. **`AlterTableAddCheck` cascade `PartitionChildren` → full descendant set**
   (`operators_ddl.go:~8740-8780`) — this slice's own deferral, and it converts
   the retained vacuous port into a real guard. NOTE: `collectDMLPartitionLeaves`
   (`operators_storage.go:2869`) is leaves-only/partitions-only and NOT reusable —
   an ALTER cascade must hit intermediate nodes AND inheritance children. The
   correct node set is an open design question; that is the slice's real work.
2. Cheap cleanup, bundle with any slice touching that file: re-join 0005an's split
   `parent_noinh_convalid` fixture
   (`operators_ddl_check_validate_cascade_test.go:212-226`) into a literal upstream
   port — its `DELETE FROM ONLY` blocker is gone as of 0005ao.
3. `validateCheckConstraintRows` whole-tree → per-relation walk (0005an row): PG
   skips re-scanning an already-`convalidated` descendant (`tablecmds.c:12960`).
4. **TRAP — do not size from the diff:** `ADD GENERATED … AS IDENTITY` looks
   ~2 lines but does not parse at all; closing it = porting `ATExecAddIdentity`
   (`tablecmds.c:8240-8362`), a feature slice. Ledgered 5×.
5. Not recommended: hunk 6 (circle gist opclass), hunk 5 (`SET CONSTRAINTS ALL
   IMMEDIATE`), the generic error-text/formatting hunks (known trap).

**Standing finding (unchanged).** goopg still has NO one-shot `find_all_inheritors`.
Three walks now exist with different node sets and epoch semantics:
`collectDMLPartitionLeaves` (DML, snapshot epoch), `allDescendants`
(`operators_fk.go:1004`, FK/RI, current epoch), and the ALTER cascade's flat
`PartitionChildren`. Item 1 above will need a fourth or a unification — real work,
not a rename.

**Delegation:** none active. `tmp/ralph-handoffs/m0134-0005ar{,-research}/` closed
(DONE). **In-flight:** none.
