# ALTER STATISTICS … SET STATISTICS round-trip in pg_dump (DU-002 slice 317)

- Milestone/spec: M0119-0004 (pg_dump 002–010 / DU-002 catalog-view parity battery)
- Status: accepted
- Oracle: `postgres/src/bin/pg_dump/pg_dump.c` `getExtendedStatistics` /
  `dumpStatisticsExt`; `postgres/src/backend/commands/statscmds.c`
  `AlterStatistics`.

## Problem

Slices 314/316 made `CREATE STATISTICS` objects (simple-column and expression)
dumpable end-to-end. A statistics object can additionally carry a non-default
**statistics target** set via `ALTER STATISTICS <name> SET STATISTICS <n>`
(stored in `pg_statistic_ext.stxstattarget`). goopg had no `ALTER STATISTICS`
statement at all — the SQL errored at parse time — and the `pg_statistic_ext`
virtual row hardcoded `stxstattarget = -1`, so a target could never be recorded
and pg_dump never re-emitted the `ALTER`. A dump/restore silently lost the
target.

## Oracle behavior

`getExtendedStatistics` (pg_dump.c:8076) selects `stxstattarget` (NULL on
remote < v13). It maps a NULL result to `-1`:

```c
if (PQgetisnull(res, i, i_stattarget))
    statsextinfo[i].stattarget = -1;
else
    statsextinfo[i].stattarget = atoi(PQgetvalue(res, i, i_stattarget));
```

`dumpStatisticsExt` (pg_dump.c:18318) emits the `ALTER` only for a non-default
target:

```c
if (statsextinfo->stattarget >= 0)
{
    appendPQExpBuffer(q, "ALTER STATISTICS %s ", fmtQualifiedDumpable(...));
    appendPQExpBuffer(q, "SET STATISTICS %d;\n", statsextinfo->stattarget);
}
```

So in PG18 the default is `stxstattarget = NULL` → `-1` → no `ALTER`; a value
`>= 0` (including `0`, which disables sampling) re-emits the `ALTER`. The valid
range is `-1` (reset to default) .. `10000`.

## Change

End-to-end capture of the per-object statistics target:

- **Parser** (`internal/parser/ast.go`, `ddl.go`): new `AlterStatisticsStmt`
  (`Name`, `IfExists`, `Target int`, `HasTarget bool`). `parseAlter` gains an
  `ALTER STATISTICS` branch (after the `ALTER SEQUENCE` branch). It accepts the
  optional `IF EXISTS`, the (possibly schema-qualified) object name, then `SET
  STATISTICS n` — parsing a leading `-` so `-1` is captured as a reset. Other
  `ALTER STATISTICS` forms (`RENAME TO` / `OWNER TO` / `SET SCHEMA`) parse to the
  same node with `HasTarget=false` and are consumed as no-ops.
- **Catalog** (`internal/catalog/catalog.go`): `StatisticsObject.StatTarget
  *int` (nil = default). New lock-safe `SetStatisticsTarget(name, *int) bool`.
  The `pg_statistic_ext` virtual row projects `stxstattarget` from `StatTarget`:
  a non-nil value verbatim, otherwise the pg_dump-equivalent default `-1`.
- **Executor** (`internal/executor/operators_ddl.go`): dispatch case +
  `execAlterStatistics`. A `SET STATISTICS n` with `n >= 0` records `&n`; a
  negative `n` (−1) resets to nil. Dump-fidelity only — goopg neither computes
  nor consumes extended statistics, mirroring the per-column `SET STATISTICS`
  treatment (DU-002 slice 184).
- **Planner** (`internal/planner/planner.go`): `AlterStatisticsStmt` routed to
  the `DDL` node. **Command tag** (`internal/server/dispatch.go` `ddlTag`):
  `ALTER STATISTICS` (and `CREATE STATISTICS`, previously falling through to the
  generic `OK`).

### Why `-1` and not a true NULL for the default

The string-based `VirtualRows` machinery has no integer NULL sentinel:
`planner.TypedVirtualCell` parses an empty `int4` cell as a `StringConst ""`,
which pg_dump reads as `atoi("") = 0` → a spurious `SET STATISTICS 0`. Emitting
`-1` is byte-identical to NULL for pg_dump's purpose (`getExtendedStatistics`
maps both to `-1` → no `ALTER`) and matches the prior hardcoded value, so a
direct `SELECT stxstattarget` is unchanged from before this slice. A faithful
int-NULL projection would require widening `TypedVirtualCell`'s integer branch
(broad blast radius across every int catalog column) and is out of scope.

Blast radius nil: the new field defaults nil; the virtual row is unchanged for
every existing object; TPC-H/pgbench carry no statistics objects.

## Verification

- **DU-002 slice 317** in `TestPort_PgDumpConnectionSetup`: `ALTER STATISTICS
  public.statext_nd SET STATISTICS 250` re-emits `ALTER STATISTICS
  public.statext_nd SET STATISTICS 250;`; the default-target objects
  (`statext_all`/`statext_expr`/`statext_mix`) emit **no** `ALTER STATISTICS …
  SET STATISTICS` line. Asserted byte-identical vs **real pg_dump 18.3** (4.5 s)
  PASS.
- Unit `TestParseAlterStatisticsSetStatistics` (parser: 100 / 0 / −1 / schema-
  qualified / `IF EXISTS` / no-op `RENAME` form).
- Unit `TestSetStatisticsTargetProjection` (catalog: default −1, SET 250, SET 0,
  reset, unknown-object false).
- `internal/parser` + `internal/catalog` + `internal/planner` +
  `internal/executor` + `internal/server` suites PASS; `go build ./...` clean;
  pgbench TPC-B smoke = pre-commit hook.

## Still open under M0119-0004

The broader pg_dump 002–010 catalog-view parity battery (further DU-002 slices);
extended-protocol commit-time deferral (architecturally entangled — the extended
protocol is auto-commit-per-statement).

## Follow-up

See `docs/design/0110-0001-pg-dump-tap-port.md` ("Follow-up: `ALTER
STATISTICS … RENAME TO / OWNER TO / SET SCHEMA` were silent no-ops (DU-002
slice 441)") — the DU-002 ALTER-form-gap slices (439/440/441) are tracked
there rather than per-object-type design docs.
