# 0118-0054 — `plpgsql-toast` PROMOTED: FOR-loop snapshot stability + record-field substitution in SELECT INTO

* Milestone: **M0118-0008** (DDL / VACUUM / maintenance concurrency isolation specs)
* Spec: `postgres/src/test/isolation/specs/plpgsql-toast.spec`
* Status: **accepted — promotion** (the last blocker of the spec; it now passes
  byte-for-byte, hard-asserted via `runIsoSpecStrict`)
* Predecessors: 0118-0044 (dollar-quote-aware splitter), 0118-0049 (PL/pgSQL
  transaction control / `COMMIT` in DO), 0118-0050 (`SELECT … INTO`), 0118-0051
  (scalar subquery), 0118-0052 (record field assign + `record::text`), 0118-0053
  (record FOR-loop `record::text` framing)

## Summary

`plpgsql-toast` exercises PL/pgSQL variable assignment code paths under
transaction control: each `assignN` step does an assignment, `DELETE`s the source
row, `COMMIT`s, then waits on an advisory lock so a second session's `VACUUM`
runs *between* the variable's last write and its use, finally printing
`length(var)`. In PostgreSQL the point is that a PL/pgSQL variable must hold a
**detoasted** value across the commit, or the `VACUUM` would orphan its external
TOAST chunks ("missing chunk number …").

After 0118-0049..0053 the first five assignment paths (`assign1`–`assign5`)
already matched PG. This loop closed the final two divergences and promoted the
spec:

1. **`assign6` — FOR-loop snapshot stability across `COMMIT`.** The loop
   `for r in select test1.b from test1 loop … delete from test1; commit; … end
   loop` must iterate over all three rows fetched at loop start even though the
   body deletes every row and commits each iteration. goopg streamed rows from a
   **live operator**, calling `op.Next()` between body executions, so after the
   first iteration's `DELETE`/`COMMIT` the scan returned EOF and the loop ran
   only once (one notice `6002` instead of `6002 9002 12002`).

2. **`fetch-after-commit` — record-field reference in embedded SQL.** The body
   `select b into t from test1 where a = r.a` references the loop record field
   `r.a`. goopg planned the query verbatim and failed with
   `42P01: missing FROM-clause entry for table "r"` because the
   `SELECT … INTO` path performed **no** PL/pgSQL variable substitution and the
   substitutor skipped qualified names anyway.

goopg stores `text` inline (no external TOAST pointer that a `VACUUM` could
orphan), so the *detoast* correctness the spec guards is satisfied structurally —
the remaining work was purely the two control/scoping gaps above.

## Changes (all in `internal/executor/plpgsql_runtime.go`)

### 1. Materialize FOR-query rows up front

`ForSelectStmt` now drains the operator into a `[]forRow` (each row deep-copied,
since `slot.Row()` may alias a reused buffer), closes the operator, and *then*
runs the loop body per collected row. This mirrors PostgreSQL, where a PL/pgSQL
implicit FOR-query cursor holds its snapshot for the life of the loop and is made
**holdable** when a `COMMIT` occurs inside the body, so iteration is unaffected by
the body's `DELETE`/`COMMIT` side effects. It is also strictly more PG-faithful
for the no-commit case: a body that modifies the scanned table no longer sees its
own writes mid-scan. The binding logic (record-composite vs scalar vs sub-field)
is unchanged — only its inputs moved from a live `slot` to a materialized row.

The three `op.Close()` calls that were inside the per-row body handling were
removed (the operator is now closed once, before the body loop).

### 2. Substitute frame variables — including `r.field` — in `SELECT … INTO`

The `SelectIntoStmt` path now calls `substitutePlpgsqlFrameVarsInSQL(sql, frame)`
before parsing/planning, exactly as the general embedded-SQL path
(`execPLpgSQLEmbeddedSQL`) already did. This was simply missing for `SELECT …
INTO`, so scalar *and* record-field references in its `WHERE`/projection went
unsubstituted.

`substitutePlpgsqlFrameVarsInSQL` previously skipped any identifier that is part
of a qualified name (preceded/followed by `.`). It now handles a single-level
**record-field reference** `r.field` when `r` is a record/composite PL/pgSQL
variable (`frame.isRecordVar`): it substitutes the whole `r.field` token with the
SQL literal of the field, which is bound as `_<var>_<field>` in the frame by
`bindRecordRowComposite`. Guards keep blast radius nil:

* only fires when the leading identifier `isRecordVar` — a plain `table.column`
  qualifier (the overwhelmingly common case) is untouched;
* requires a simple `.ident` follower with no further `.`/`[` (no `r.a.b`,
  no `r.a[1]`), falling through to the existing behavior otherwise;
* the field literal comes from `datumToSQLLiteral`, the same formatter the scalar
  substitution path uses.

## Why this is correct / faithful

* **Snapshot stability.** PG's PL/pgSQL `exec_stmt_fors` opens a portal and
  fetches against a fixed snapshot; on `COMMIT` inside a procedure the portal is
  preserved (holdable). Materializing the full result at loop start is a faithful
  realization for goopg's executor model and matches the observed outputs
  (`6002 9002 12002`).
* **Variable substitution.** PG passes PL/pgSQL variables to embedded SQL as
  parameters, so `r.a` resolves to the record field, never a table reference.
  Textual substitution of `r.field` → literal reproduces that for the cases the
  spec needs; it is the same mechanism the existing INSERT/UPDATE embedded-SQL
  path uses for scalars, extended to qualified record fields.
* **TOAST.** goopg keeps `text` inline; there is no external chunk for `VACUUM`
  to free, so the "missing chunk" failure mode the spec defends against cannot
  occur. The advisory-lock/`VACUUM` choreography still runs and the runner's
  300 ms timing threshold renders the `<waiting …>` markers correctly (the
  `perform pg_advisory_lock(1)` genuinely blocks behind session s2's held lock).

## Blast radius

Executor-only; no parser/planner/storage/catalog change. The FOR-loop change
affects every PL/pgSQL `FOR … IN SELECT` loop (now snapshot-stable — strictly
more correct), and the substitution change affects only `SELECT … INTO` inside a
PL/pgSQL body (previously unsubstituted, now consistent with other embedded SQL)
plus the new `r.field` qualified-record case. No hot SQL path touched; pgbench
smoke unaffected.

## Tests / gates

* `TestPort_IsolationPlpgsqlToast` — **strict PASS** (all 7 permutations
  byte-for-byte vs PG 18.3).
* `TestPlpgSQLForLoopMaterializeAndRecordFieldSubst` (new, executor unit) —
  (1) a FOR loop whose body `DELETE`s the scanned table still runs 3 times;
  (2) `select b into v … where a = r.a` returns `6000 9000 12000`.
* `TestPlpgSQLRecordForLoopAndText` / `…RecordFieldAndText` / `…SelectInto` /
  `…ScalarSubquery` / `…DoCommitChain*` — PASS (no regression).
* Full `internal/executor` package — PASS.
* `TestPort_IsolationSubxidOverflow` / `…FreezeTheDead` — PASS (sibling
  PL/pgSQL/DO-block specs, no regression).
* `go build ./internal/executor` clean; pgbench smoke = pre-commit hook.

## Follow-ups

None for `plpgsql-toast` — fully promoted. Remaining M0118-0008 tail:
`alter-table-4` (INHERITS + transactional-DDL cross-session visibility),
`detach-partition-concurrently-{1,2,3,4}` + `partition-concurrent-attach`
(transactional partition visibility), `partition-drop-index-locking` (pg_locks
view parity), `reindex-concurrently-toast` (TOAST relations as catalog objects +
`allow_system_table_mods`). The group stays open.

A latent niceness not needed by any `port` spec: `record_out` quote/escape
framing (the current composite join is unquoted — correct for `repeat('foo',N)`
payloads, would differ for values containing `,`/`(`/`"`).
