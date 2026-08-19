# Working set — M0134-0005as LANDED (ALTER ADD CHECK full-descendant cascade)

**Task:** M0134-0005 / sub-item **M0134-0005as** — LANDED, committed, pushed.
One researcher + one implementer (2 rounds) + one tester round, zero escalations,
one non-blocking deviation. No resume needed.

**Selection:** M-NIGHTLY had nothing new — `ci/logs/action-items.md` still at run
20260819-011823, whose only `AI-…-001` is filed AND `[x]` fixed. M0134 next per the
banner; took the baton's ranked #1.

**What landed.** `AlterTableAddCheck`'s cascade (`operators_ddl.go:8752-8781`) was a
flat one-level loop over `PartitionChildren`: no plain-INHERITS child, no grandchild
of any kind. Replaced by new `cascadeCheckToChildren`/`…At` (beside the DROP twin
`cascadeCheckDropToChildren`, `:12220`), copying `cascadeNotNullToChildren`'s shape —
`collectInheritanceAndPartitionChildren` per level + per-EDGE visited guard +
`maxNotNullCascadeDepth`. Mirrors PG's `ATAddCheckNNConstraint`
(`tablecmds.c:9911-10049`): one level via `find_inheritance_children`, then DFS.
+87/−34 across 2 files + 1 new test file.

**Two latent bugs the widening surfaced, both fixed.**
1. **No `NoInherit` early return.** PG returns at `:10004` *before* enumerating any
   child. goopg had no gate — accidentally safe only because the partitions-only walk
   was empty whenever NoInherit held. Adding inheritance children destroys that
   accident; the gate is now explicit.
2. **Empty constraint name.** The cascade passed raw `act.ConstraintName`, so an
   anonymous `ADD CHECK (x>0)` propagated `""` to every child. `o.autoCheckName`
   resolution hoisted and now shared with the validation scan.

**THE REUSE TRAP (record this).** `allDescendants` (`operators_fk.go:1004`) has the
IDENTICAL reachability set and looks like the obvious reuse — but it dedups per
**node**, and PG's `coninhcount` counts once per inheritance **EDGE**
(`heap.c:2774-2845`). A diamond descendant is legitimately visited twice. Never
substitute a node-deduped walk into a DDL cascade.

**Vacuous port flipped.** `TestCheckInheritFlagsPortedAlterTableInheritedNotValid` was
0005ar's retained no-op pin; round 2 rewrote its stale comment and made it assert
`cif_attmp6/7` carry `b_le_20` with `NotValid=true, IsLocal=false, InhCount=1`.
FAIL-pre proven.

**Metric:** `constraints.sql` unchanged at **176 lines / 8 hunks** — it exercises no
inherited ALTER-added CHECK. Paid for by correctness + unblocking `alter_table.sql`.

**Gates run:** build+vet clean; 5/5 new `TestCheckAddCascade*` PASS (3 FAIL-pre proven
by stashing only `operators_ddl.go`); round-2 10/10 PASS;
`RALPH_PRECOMMIT_SCOPE=units` PASS (`internal/initdb` 412s — cold cache, not a
regression); **`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35)**; pgbench smoke via
hook.

**Next step:** continue M0134-0005 at the **176/8** baseline. Ranked:
1. **`ONLY`-with-existing-children rejection** (this slice's own deferral) —
   `ALTER TABLE ONLY p ADD CHECK …` with children must raise `constraint must be
   added to child tables too` (`tablecmds.c:10020-10023`); goopg silently adds to the
   parent alone. Needs the `s.Only` plumbing shared with other ALTER subcommands.
2. Bundle with #1 (same lines): parent's `AddCheckFull` still stores the raw
   (possibly empty) `act.ConstraintName` while children now get the resolved one —
   pass `conName` to both `AddCheckFull` and `allocConstraintOID`, then re-check
   `pg_constraint` / pg_dump constraintdef output.
3. Cheap cleanup, bundle with any slice touching that file: re-join 0005an's split
   `parent_noinh_convalid` fixture
   (`operators_ddl_check_validate_cascade_test.go:212-226`) into a literal upstream
   port — its `DELETE FROM ONLY` blocker is gone as of 0005ao.
4. `validateCheckConstraintRows` whole-tree → per-relation walk (0005an row): PG skips
   re-scanning an already-`convalidated` descendant (`tablecmds.c:12960`).
5. **TRAP — do not size from the diff:** `ADD GENERATED … AS IDENTITY` looks ~2 lines
   but does not parse at all; closing it = porting `ATExecAddIdentity`
   (`tablecmds.c:8240-8362`), a feature slice. Ledgered 5×.
6. Not recommended: hunk 6 (circle gist opclass), hunk 5 (`SET CONSTRAINTS ALL
   IMMEDIATE`), the generic error-text/formatting hunks (known trap).

**Standing finding (UPDATED — now four walks, tabulated in the design addendum).**
`collectDMLPartitionLeaves` (partitions/leaves only, snapshot epoch),
`allDescendants` (all descendants, per-NODE guard, current epoch),
`collectInheritanceAndPartitionChildren` (one level), and the DDL cascades'
transitive per-EDGE recursion. Still no one-shot `find_all_inheritors`; the node-set
differences are deliberate, not drift.

**Delegation:** none active. `tmp/ralph-handoffs/m0134-0005as{,-research}/` closed
(DONE). **In-flight:** none.
