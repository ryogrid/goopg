# 0134-0004 — `cluster.sql` regress divergence: classification, park decision, and the CREATE TABLE owner fix

Status: **case PARKED**, one bounded slice landed (Bucket 4).
Date: 2026-08-18. Task: M0134-0004 (`cluster.sql`).

## 1. Why this doc exists

M0134 digests upstream regress cases that goopg currently fails. For each case
the loop must decide one of two things: *this case has a bounded slice worth
landing*, or *this case is gated on a large coupled feature and must be parked
with an executable re-arm trigger*. This doc records that decision for
`cluster.sql`, plus the one general-purpose bug the classification pass
uncovered.

**Measurement discipline.** The regress harness changed on 2026-08-18
(M0134-0002 C19): it now passes `-v HIDE_TABLEAM=on -v HIDE_TOAST_COMPRESSION=on`
the way `pg_regress` does. **Every `cluster` line count recorded before that date
is invalid.** The numbers below were measured from scratch after the fix.

## 2. Baseline

```
scripts/pg-regress-runner.sh --verbose cluster
```
→ **0/1 PASS (0.0% parity)**. Diff at `tmp/regress-diffs/cluster.diff`:
**5747 lines** (5223 `+` / 257 `-`). A single hunk
(`@@ -511,46 +341,5047 @@`) accounts for roughly 4700 of them.

## 3. Bucketed classification

| # | bucket | ~lines | verdict |
|---|---|---|---|
| 1 | `CLUSTER` is a no-op stub — no physical index-ordered heap rewrite | ~4900 | **LARGE/COUPLED** |
| 2 | old-style `CLUSTER indexname ON tablename` syntax unparsed | ~15-25 | bounded |
| 3 | `SUBSTRING(str FOR n)` without `FROM` unsupported | ~10 | bounded, trivial |
| 4 | `CREATE TABLE` never records the creating role as owner | ~150-200 | **bounded — landed, see §5** |
| 5 | `maintenance_work_mem` GUC unregistered | ~10 | bounded, trivial |
| 6 | nested default-partition conflict misfires | ~15-20 | likely bounded, **unconfirmed** |

### Bucket 1 — the dominant one, and the reason for the park

`internal/executor/operators_cluster.go:1-97` says so in its own doc comment:
"no physical index-ordered heap rewrite is performed". `clusterOp.Next()` checks
existence, takes locks, and updates `pg_index.indisclustered` — it never
tuplesorts the heap, never swaps the relfilenode, never rebuilds the indexes.
Every subsequent `SELECT` in `cluster.sql` therefore returns rows in the wrong
physical order, and the whole tail of the expected output diverges.

PG oracle: `postgres/src/backend/commands/cluster.c` —
`cluster_rel` → `rebuild_relation` → `copy_table_data` → `finish_heap_swap`.

This is a VACUUM-FULL-scale feature (tuplesort over the heap in index order, new
relfilenode, index rebuild, catalog swap, WAL for all of it). It is not a slice.

### Buckets 2, 3, 5 — bounded but inert

All three are real gaps and each is a cheap parser/GUC addition. None of them
materially shrinks the diff, because Bucket 1 governs the output of every
statement downstream of the first `CLUSTER`. Landing them would move the line
count by tens out of 5747 while spending a loop. They are recorded here so a
later loop working an adjacent case can pick them up opportunistically.

### Bucket 6 — not confirmed

`validateDefaultPartitionConflict`
(`internal/executor/operators_ddl_partition.go:854-871`) looks correct read in
isolation; the suspicion is upstream `parent` resolution for
`PARTITION OF <sub-parent> DEFAULT`. Explicitly marked **inferred, not
verified** — do not brief an implementer from this line without one more
caller-chain read.

## 4. Park decision

**`cluster.sql` is PARKED**, consistent with M0134-0001/-0002/-0003.

**Re-arm trigger:** a real `CLUSTER` implementation — tuplesort of the heap in
index order, relfilenode swap, index rebuild — lands as its own milestone. At
that point re-run `scripts/pg-regress-runner.sh cluster` from scratch and
re-classify; buckets 2/3/5/6 are expected to become the entire residual and the
case should then be finishable in one or two slices. No design doc for a real
CLUSTER exists yet; writing one is the first step of that milestone.

## 5. Bucket 4 — the general bug, and what landed

The classification pass turned up a bug that has nothing to do with `CLUSTER`
and everything to do with privileges.

### Symptom

A non-superuser role that creates a table gets a false `42501 permission denied`
on its own table. In `cluster.sql` this shows up under
`SET SESSION AUTHORIZATION`, but it applies to any role-based workload.

### Root cause

`catalog.Table.Owner` (`internal/catalog/catalog.go:611-621`) is the field the
privilege gates consult, and no `CREATE TABLE` path ever set it:

- `dmlPrivilegePermittedAs` (`internal/executor/operators_storage.go:2222-2242`)
  — the SELECT/INSERT/UPDATE/DELETE gate — does
  `if tbl.Owner != "" && EqualFold(tbl.Owner, checkRole) { allow }`, otherwise
  falls through to `ctx.Catalog.HasTablePrivilege(...)`.
- `maintenancePermitted` (`internal/executor/operators_vacuum.go:333-342`) —
  the VACUUM/ANALYZE/CLUSTER gate — has the identical shape.
- `HasTablePrivilege` (`internal/catalog/catalog.go:16351-16366`) is
  **default-deny**: no ACL entry ⇒ `false`.

So with `Owner == ""` the owner-shortcut can never fire and the creating role is
denied unless someone hands it an explicit GRANT.

Interestingly, `CREATE VIEW` already got this right —
`execCreateView` (`internal/executor/operators_ddl.go:5391`) stamps
`vt.Owner = o.ctx.NonSuperuserRole`. This is the project's recurring
**sibling-paths-must-agree** failure mode: one member of a family was wired and
the others were not.

PG oracle: `postgres/src/backend/commands/tablecmds.c:DefineRelation` →
`postgres/src/backend/catalog/heap.c:heap_create_with_catalog`, whose `ownerId`
argument is `GetUserId()`.

### Why the fix is safe (verified, not assumed)

An exhaustive `find_referencing_symbols` pass over `catalog.Table.Owner` found
no read site where empty means "allow everyone". The only allow-on-empty
branches in the two gates are keyed on the **session's** role
(`ctx.NonSuperuserRole == ""` / `checkRole == ""`), which this change does not
touch. Therefore populating `Owner` can only convert a wrong DENY into an ALLOW
for the creating role; a third-party non-owning role's outcome is bit-for-bit
unchanged (`EqualFold` is false either way, and both paths fall through to the
same default-deny lookup). A negative-guard test pins this.

### The assignment, and why not `currentDDLOwnerName()`

```go
if o.ctx.NonSuperuserRole != "" {
    tbl.Owner = o.ctx.NonSuperuserRole
}
```

`currentDDLOwnerName()` (`internal/executor/operators_ddl.go:949-954`) returns
the literal string `"postgres"` for the bootstrap superuser. But
`catalog.Table.Owner`'s documented sentinel for "owned by the bootstrap
superuser" is the **empty string** — that is what
`ALTER TABLE ... OWNER TO CURRENT_USER` stores
(`internal/executor/operators_ddl.go:7625-7626`). Using
`currentDDLOwnerName()` would have stamped `"postgres"` onto essentially every
table in the test corpus, gratuitously diverging from the sentinel convention
for zero behavioral gain. The guarded form leaves the superuser case
zero-valued, so existing tables are untouched.

### Sites changed (three independent constructions)

| function | file:line | note |
|---|---|---|
| `execCreateTable` | `internal/executor/operators_ddl.go` ~3263 | also covers `CREATE TEMP TABLE` |
| `execCreateTableAs` | `internal/executor/operators_ddl.go` ~4278 | CTAS — independent construction |
| `execCreatePartitionChild` | `internal/executor/operators_ddl.go` ~4404 | `PARTITION OF` — a *third* site |

The third site is worth calling out: the initial brief assumed `PARTITION OF`
was handled inside `execCreateTable`, but `s.PartitionOf != nil` returns early
into `execCreatePartitionChild`, a separate top-level function with its own
`Catalog.CreateTable` call. The implementer caught the error and the brief was
widened rather than shipping a fix that worked for plain tables but not for
partition children. Sibling-path discipline again.

`ALTER TABLE ... ATTACH PARTITION` (`operators_ddl.go:8134`) operates on an
already-existing table and is **not** a construction site. Table-creating
`SELECT INTO` is not implemented in goopg at all.

### Guards

`internal/executor/create_table_owner_test.go`:

- `TestCreateTableStampsCreatingRoleAsOwner` — FAIL-pre / PASS-post
- `TestCreateTableAsStampsCreatingRoleAsOwner` — FAIL-pre / PASS-post
- `TestCreateTablePartitionOfStampsCreatingRoleAsOwner` — FAIL-pre / PASS-post
- `TestCreateTableOwnerNegativeGuardDeniesOtherRole` — **PASS both pre and
  post**; this is the test that proves the change is additive rather than
  allow-all
- `TestCreateTableBootstrapSuperuserOwnerEmpty` — asserts the `""` sentinel
  survives for superuser-created tables

## 6. What is still deferred (see `.ralph/deferral_ledger.md`, 2026-08-18)

1. **Table ownership does not survive a restart.** `catalog.Table.Owner` is
   in-memory only. `pg_class.relowner` is hardcoded to `bootstrapSuperuserOID`
   for every user table (`internal/executor/pg18_user_catalog_rows.go:563, 672,
   2013`) — deliberately, per `catalog.go:611-621` — and is never synced
   from or to `tbl.Owner` in either direction. Neither writer of `Owner`
   (`ALTER TABLE OWNER TO`, and now CREATE TABLE) emits WAL or a heap write. So
   a role that owns a table before a restart loses owner-privilege on it after,
   until `ALTER TABLE ... OWNER TO` is re-run. This gap pre-existed for
   `ALTER TABLE OWNER TO`; this change widens the affected population to every
   table created under `SET SESSION AUTHORIZATION` / `SET ROLE`.
2. **`CREATE DATABASE ... TEMPLATE` drops owners.**
   `internal/postmaster/database_ddl.go:917-923` builds a fresh
   `catalog.Table` per cloned table without copying `srcTbl.Owner`. Same failure
   shape as Bucket 4, different subsystem — needs its own slice.
3. **The no-storage CTAS fallback** in `execCreateTableAs` (~4225-4232)
   discards the returned `*catalog.Table`, so it cannot stamp an owner without
   capturing the return value. Edge case (no live storage pool), untouched.
4. Buckets 1, 2, 3, 5, 6 above.

## 7. References

- Handoffs (scratch, not the record):
  `tmp/ralph-handoffs/m0134-0004-s01-measure/`,
  `tmp/ralph-handoffs/m0134-0004-s02-create-table-owner/`
- Sibling case docs: `docs/design/0134-0003-arrays-sql-divergence.md`,
  `docs/design/0134-0001-p2-explain-format.md`
