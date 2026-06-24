# 0118-0075 — ALTER TABLE ATTACH PARTITION enforces the default-partition conflict check (M0118-0008)

Status: accepted
Spec: `partition-concurrent-attach` (M0118-0008 hard tail) — **enabler, NOT a promotion.**

## Problem

`ALTER TABLE parent ATTACH PARTITION child FOR VALUES …` must reject the attach
when the parent's existing **DEFAULT** partition already holds (committed /
visible) rows that the new partition's bounds would now claim — PostgreSQL's
`ATExecAttachPartition` → `check_default_partition_contents`
(`src/backend/commands/tablecmds.c`), SQLSTATE `23P01`,
`updated partition constraint for default partition "X" would be violated by some row`.

goopg already enforced this on the `CREATE TABLE … PARTITION OF` path
(`validatePartitionChild` → `checkDefaultPartitionDataConflict`,
`operators_ddl.go:3341`), but the `ALTER TABLE … ATTACH PARTITION` executor case
(`operators_ddl.go`, `parser.AlterTableAttachPartition`) skipped it entirely:
the attach silently succeeded, leaving rows in the default that violate its
updated partition constraint. This is a real PG-faithfulness gap independent of
concurrency, and it is the standalone core of the `partition-concurrent-attach`
isolation spec (perm 3's `ERROR: updated partition constraint for default
partition "tpart_default_default" would be violated by some row`).

## Change

1. **Wire the check into the ALTER ATTACH path** (`operators_ddl.go`,
   `AlterTableAttachPartition` case): after resolving the child relation and
   before registering it, call `checkDefaultPartitionDataConflict(childTbl.Name,
   tbl, poc, act.Pos(), o.ctx)` and `return err` on failure. Gated
   `!poc.Default && !poc.IsHash` (attaching the default itself, and HASH
   strategy which forbids default partitions, are exempt) — identical predicate
   to the CREATE path.

2. **Leaf-default naming** (`operators_ddl_partition.go`,
   `checkDefaultPartitionDataConflict`): the error now names the **leaf** default
   partition. After locating the parent's immediate default, the function walks
   down through any sub-partitioned default's own default (recursively) to the
   deepest default descendant and uses that name in the `23P01` message. This
   mirrors PG's `check_default_partition_contents` recursing into a partitioned
   default and naming the specific leaf default (`tpart_default_default`, not the
   intermediate `tpart_default`). Detection still scans the whole default subtree
   via the immediate default partition (a partitioned relation expands to all its
   descendants in the inline `SELECT 1 FROM <default> WHERE <bounds> LIMIT 1`),
   so the existing detection coverage is unchanged; only the reported name is
   refined. The non-nested case (immediate default == leaf default) is
   unaffected, so the shared CREATE path keeps identical behaviour there.

## Why this is an enabler, not a promotion

`partition-concurrent-attach` remains `defer`. The full spec couples three
subsystems that must all land together to match PG byte-for-byte:

- **Deferred-until-commit attach visibility** — s2 must not see `tpart_2` until
  s1 commits, so its INSERT routes to the default (transactional DDL / per-session
  catalog visibility, the same blocker shared with `alter-table-4`).
- **ATTACH locks the default partition** (AccessExclusive) so the concurrent
  INSERT routed to the default **waits** (`<waiting ...>`) until the attach txn
  commits.
- **Constraint re-validation after the wait** — only after seeing the other
  session's committed rows does the conflict check (this change) or the routed
  INSERT's partition-constraint check fire.

This change lands the third piece (the conflict check itself) as an
independently-correct, plain-SQL-testable feature. The visibility + locking
pieces are recorded in the deferral ledger as the remaining work.

## Tests / gates

- New `internal/executor/attach_default_conflict_test.go`:
  - `TestAttachPartitionRejectsDefaultConflict` — RANGE default holds a row in
    `[100,200)`; ATTACH of that range fails `23P01` naming `tp_def`.
  - `TestAttachPartitionNoConflictSucceeds` — negative control (row outside the
    range ⇒ attach succeeds).
  - `TestAttachPartitionNestedDefaultNamesLeaf` — mirrors the spec's
    sub-partitioned default; error names the LEAF `tpart_default_default`.
- `go test ./internal/executor/` PASS (full package, no regression).
- `TestPort_IsolationDetachPartitionConcurrently1` strict PASS (shares partition
  attach infra in its setup — no false positive from the new check).
- `go build ./...` clean.
- pgbench smoke = pre-commit hook.

## Oracle

`postgres/src/backend/commands/tablecmds.c` — `ATExecAttachPartition`,
`QueueCheckPartitionConstraint` / `check_default_partition_contents`.
