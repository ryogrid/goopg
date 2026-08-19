# Working set — M0134-0005al LANDED (nullable PRIMARY KEY column)

**Task:** M0134-0005 / sub-item **M0134-0005al** — LANDED, committed, pushed.
Clean slice, one implementer round, zero deviations. No resume needed.

**Selection:** M-NIGHTLY had nothing new (`ci/logs/action-items.md` still at run
20260819-011823, its only `AI-…-001` already filed AND fixed at fix_plan :1192);
M0134 next per the banner.

**What landed.** `ALTER TABLE t ALTER COLUMN a DROP NOT NULL` on a PK column
returned success and cleared the flag — a **nullable PRIMARY KEY**, a catalog
hole, not a wording hunk. The guard was not overlooked, it was implemented on
the **wrong twin**: Bucket 3 (2026-08-18) put PG's PK refusal on the
DROP-**by-name** path and its own ledger row named the by-column arm as
"equally unguarded" to fix in the same pass — it wasn't. Third occurrence of
this campaign's shape: the guard exists, the grep hits, on the sibling.
PG keeps the check in `dropconstraint_internal` (`tablecmds.c:14128-14159`),
NOT `ATExecDropNotNull` — four ordered refusals, PK first, before
replica-identity/identity/`attnotnull`-clear. Fix: `IndexesOnTable` +
`idx.Primary`/`idx.Columns` walk in the `case parser.AlterTableDropNotNull:`
arm (`operators_ddl.go:10491`), before `clearNotNullConstraint` (guard in the
case arm, NOT the helper — 3 callers). `42P16`, no detail/hint — **not** the
`55000` of the adjacent `verifyNotNullPKCompatible`.

**Measurement: constraints 188/9 -> 176/8.** Baseline for the next loop is
**176/8**. Never compare to a pre-2026-08-19 number.

**Gates run:** `go build ./...` PASS; `go test ./internal/executor/
./internal/catalog/` PASS; 4x `TestAlterTableDropNotNullOnPrimaryKey*`
FAIL-pre/PASS-post (re-verified by the coordinator's tester before commit);
`RALPH_PRECOMMIT_SCOPE=units` PASS; regress `constraints` 176/8 with no hunk new
or grown; pgbench smoke via hook. No tpch-spotcheck: DDL guard only, no
planner/executor row-path change.

**Next step:** continue M0134-0005 at the **176/8** baseline. Ranked remaining
(from the 0005ai census, `tmp/ralph-handoffs/m0134-0005ai-research/report.md` —
reuse it, do NOT re-run the census):
1. **CHECK-constraint VALIDATE cascade** — ledgered 2026-08-19, twin of 0005ak.
   Bigger than the NOT NULL version: CHECK validation implies re-scanning each
   descendant's rows, not a flag flip. PG's `QueueCheckConstraintValidation`
   (`tablecmds.c:13117-13210`) has NO already-convalidated skip.
2. The two remaining `dropconstraint_internal` NOT NULL refusals
   (replica-identity index `:14162-14167`; identity column `:14174-14181`) —
   ledgered TWICE now; must land on **both** spellings in ONE pass (Rule #2).
   Small, and this row exists precisely because a prior slice did one side only.
3. **TRAP — do not size from the diff:** the `ADD GENERATED … AS IDENTITY` hunk
   looks ~2 lines and adjacent to what just landed, but `ALTER COLUMN … ADD
   GENERATED` does not parse at all (no parser branch / action kind / executor
   case; silent no-op). Closing it = porting `ATExecAddIdentity`
   (`tablecmds.c:8240-8362`), a feature slice. Ledgered 3x (0005n/v/w).
4. Not recommended: hunk 6 (circle gist opclass), hunk 5 (`SET CONSTRAINTS ALL
   IMMEDIATE` checked at commit not INSERT — real but large), hunks 1-4/7
   (generic error-text/formatting — known trap).

**Key finding carried forward.** goopg has NO one-shot `find_all_inheritors` —
only single-level `InheritanceChildren`/`PartitionChildren` (`catalog.go:4489`,
`:4510`) — so every cascade self-recurses, and PG's "skip an already-convalidated
descendant" prune is UNSAFE to copy verbatim (a validated intermediate would hide
an invalid grandchild). Any future cascade slice inherits this.

**Delegation:** `tmp/ralph-handoffs/m0134-0005al-{research,impl,gate}/`. All DONE,
one round each. NOTE (3rd loop running): subagents are blocked by a tool rule from
writing `report.md` and return findings inline instead — brief them to return
content inline and fold it here; the impl brief's research report DID get written.

**In-flight:** none.
