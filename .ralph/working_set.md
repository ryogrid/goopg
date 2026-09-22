# Working set — loop #18 (2026-09-22)

## BLOCKED on an owner-only harness repair — do not re-litigate, do not retry

`Task:` M0122-0015 foreign-table catalog durability — **complete, fully gated,
STAGED, not committable**.

`In-flight:` none (no gate abandoned; nothing running).

## The blocker (unchanged, re-verified loop #18)

`.githooks/commit-msg` runs under `set -euo pipefail`; its fire-set arm pipes
into `scripts/fireset-scope.sh` and then reads `fireset_rc=$?`. The script
returns **1** for "out of scope" — the common case — so `set -e` ends the hook
at that line and the commit fails with **exit 1 and no message**. Repair is one
line (`fireset_rc=0` + `|| fireset_rc=$?`); the `case` arms below it already
handle rc 0 and rc >= 2 correctly.

The loop MUST NOT apply it: the RALPH_LOOP H4 guard names `.githooks/` an
owner-only harness mechanism file and prescribes escalation. Two attempts were
refused (correctly). Probing the CI surface further is also refused — stop.

Blast radius is repo-wide: the arm runs before the `RALPH_LOOP` check, so any
committer's commit staging a non-test `.go` file outside
`internal/optimizer|planner` / `*cost*|*stat*|*selfuncs*` is unlandable.

## What is staged (the deliverable — do NOT `git add -A`, stash, or reset)

`internal/executor/{sys_pg_foreign,pg18_user_catalog_rows,operators_ddl,sys_catalog_btree_split}.go`,
`internal/initdb/{catalog_heap_reload,open}.go`,
`internal/testport/pgdump003_with_server_test.go`,
`docs/design/0100-0149/0122-0015-foreign-table-catalog-durability.md`,
`docs/design/README.md`, `.ralph/deferral_ledger.md`, `.ralph/fix_plan.md`.

Message: `/tmp/ftmsg.txt`. After the owner repairs the hook:
`git commit -F /tmp/ftmsg.txt -- <the paths above>` — **do not re-run the
gates**, every stamp is PASS against exactly this staged tree (initdb/catalog/
executor units; both 003 ports; full RegressSuite, failing set unchanged;
tpch-spotcheck Q12=2/Q13=33; sf025 PASS=96, PLAN-SHAPE same=99 changed=0;
pgbench smoke).

## Why no other task was started

Gate stamps hash the **staged** tree. While this deliverable sits in the index,
any other task's gates either cover these files or stamp FAIL on a tree/index
mismatch. Unstaging to work around that risks the only copy of a verified
loop's work. Waiting is the correct move.

## Next task once this lands

Banner item 10 → M0122-0015 continues with `010_dump_connstr.pl` (309 lines).
Ledger-open: the four foreign-data catalogs are still pinned to `DefaultDBOid`;
`CREATE FOREIGN TABLE` still returns the tag `CREATE TABLE`.

## Nightly triage

All 16 `AI-20260922-004850-*` items in `ci/logs/action-items.md` are filed in
fix_plan. Nothing unfiled (re-checked loop #18).
