# M0134-0018 — TEMP-table shadow drop must roll back when the CREATE fails

Status: implemented (slice 1 of the `create_index.sql` sizing; the case itself
is PARKED — see `.ralph/fix_plan.md` M0134-0018 and the 2026-08-20 ledger rows).

## Symptom

A failing `CREATE TEMP TABLE x ...` **permanently removes the pre-existing
permanent `public.x` from the live catalog, for every session**, until the
server is restarted. Minimal repro (no regress harness needed):

```sql
CREATE TABLE zz(i int); INSERT INTO zz VALUES (1),(2);
CREATE TEMP TABLE zz AS SELECT * FROM public.zz;
-- ERROR:  42P01: relation "zz" does not exist
SELECT count(*) FROM pg_class WHERE relname='zz';   -- goopg: 0   PG: 2
```

Found by bisecting `create_index.sql:84`
(`CREATE TEMP TABLE point_tbl AS SELECT * FROM public.point_tbl;`), which is
where `point_tbl` silently vanished mid-case. The severity is not the regress
diff: it is user-visible loss of a table from the catalog.

Scope of the loss, established by probe:

- **catalog-entry loss only** — the heap file at the original relfilenode is
  untouched on disk;
- **not session-scoped** — the entry is gone from a brand-new connection,
  because goopg's `catalog.InMemory` registry is shared cluster-wide;
- **heals on restart** — startup reloads `pg_class` from durable storage and
  re-registers the relation with its original OID/relfilenode, which is what
  makes the bug silent rather than catastrophic.

## Root cause

`execCreateTable` (`internal/executor/operators_ddl.go:1713`) handles TEMP
shadowing of a permanent relation (M0097-0003) by *destructive pre-emption*:
at `:1750-1766`, when the target name already resolves to an existing relation
and `s.Temporary` is set, it stashes the permanent `*catalog.Table` in
`o.ctx.TempTableShadows[key]` and immediately `Catalog.DropTable`s it, so the
about-to-be-created TEMP relation can take the bare-name catalog key. The
permanent entry is re-registered only by `DROP TABLE` on the temp relation
(`operators_ddl.go:6936-6945`, `im.RegisterTable(permTbl, dbOid)`).

That handshake assumes the create always succeeds. It does not:

- the dispatcher only *then* branches to `execCreatePartitionChild`,
  `execCreateTableAs` (`:4697`), or the ordinary column path — each of which
  can fail;
- for the self-shadowing CTAS above the failure is guaranteed: the drop has
  already removed `public.zz`, so `optimizer.Plan(s.SelectSource, …)` at
  `:4714` cannot resolve the SELECT's own source and returns 42P01 at `:4716`,
  **before** any `Catalog.CreateTable` call.

With no TEMP relation created, no `DROP TABLE` can ever run against it, so the
restore path is unreachable and the permanent table is stranded in the shadow
map for the rest of the process lifetime.

## Fix (this slice)

Make the shadow-drop transactional with respect to the create it precedes: if
`execCreateTable` returns an error after the drop, re-register the stashed
permanent table and clear the shadow-map entry. The restore is byte-identical
to the one `DROP TABLE` already performs, so there is one notion of "undo a
shadow", not two.

Implemented as a named-error-return `defer` in `execCreateTable`, armed only on
the branch that actually performed the drop. Properties:

- **Failure-mode complete.** It covers every post-drop error exit — CTAS,
  partition child, ordinary column path, tablespace resolution — not just the
  one `create_index.sql` happened to hit. The 2026-08-07-style trap of fixing
  the observed statement and leaving its siblings is explicitly avoided.
- **Sound by construction on the success path.** `retErr == nil` leaves
  behavior bit-for-bit as before, so no currently-passing shadowing case can
  regress; `TestDDLCreateTempTableShadowsPermanentTable`
  (`internal/executor/storage_ddl_test.go:207`) is the guard for that.

After the fix the repro above still *errors* — but with the permanent table
intact, which is the difference between a rejected statement and data loss.

## What this deliberately does NOT do (deferred, ledgered 2026-08-20)

PostgreSQL has no "shadow drop" concept at all. `CREATE TEMP TABLE zz AS
SELECT * FROM public.zz` **succeeds** there: the two relations coexist in
different namespaces (`pg_temp_N` vs `public`), and shadowing is purely a
`search_path` resolution outcome — `RangeVarGetRelid` /
`RelnameGetRelid` resolve through the active search path
(`postgres/src/backend/catalog/namespace.c`), with `pg_temp` implicitly first;
an explicitly qualified `public.zz` therefore keeps resolving to the permanent
relation even while a temp relation of the same name exists.

goopg keys `catalog.InMemory` on the bare relation name, which is why
M0097-0003 reached for destructive pre-emption instead. Making the self-
shadowing CTAS actually succeed requires namespace-keyed catalog lookup with
search-path resolution — REFACTOR-tier, and the correct end state that retires
`TempTableShadows` entirely. Ledgered with a re-arm trigger.

## Verification

- New regression test asserting the repro's post-condition: after the failing
  `CREATE TEMP TABLE … AS SELECT … FROM public.<same name>`, the permanent
  relation is still resolvable (FAIL-pre / PASS-post).
- `TestDDLCreateTempTableShadowsPermanentTable` still passes (success path
  unchanged).
- units pre-commit suite; tpch-spotcheck (Q12=2 / Q13=35) as an executor-path
  change.
