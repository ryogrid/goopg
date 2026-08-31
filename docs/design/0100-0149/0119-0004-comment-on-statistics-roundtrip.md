# 0119-0004 — COMMENT ON STATISTICS round-trip in pg_dump (DU-002 slice 315)

Status: accepted
Milestone: M0119-0004 (deferral-ledger backlog — pg_dump 002–010 / DU-002 catalog parity)

## Problem

Slice 314 made an extended-statistics object (`CREATE STATISTICS`) round-trip
through `pg_dump`. As a direct consequence two `pg_dump` getter paths became
reachable for the first time and were left **unguarded**:

1. **The CREATE STATISTICS DDL itself.** Slice 314 wired the
   parser→catalog→`pg_get_statisticsobjdef` path and created two objects in the
   `TestPort_PgDumpConnectionSetup` fixture, but never asserted the emitted DDL
   through `pg_dump`. A regression in `BuildStatisticsObjDef` (kinds suppression,
   column-list order, or schema qualification) would have gone undetected.

2. **`COMMENT ON STATISTICS`.** `pg_dump`'s `dumpStatisticsExt`
   (`src/bin/pg_dump/pg_dump.c`) calls `dumpComment` for every statistics object,
   which re-emits a `COMMENT ON STATISTICS <nsp>.<name> IS '...'` from the
   matching `pg_description` row (classoid `3381` = `pg_statistic_ext`). goopg
   already parsed `COMMENT ON STATISTICS` (`parser.go:2240`) and `execCommentOn`
   already keyed the comment under classoid `3381` via `LookupStatistics`
   (`operators_ddl.go:12332`), and the generic `pg_description` virtual table
   (`catalog.go:4759`, built from `AllComments`) already surfaced it — but with
   **no dumpable statistics object before slice 314** the `dumpComment` path was
   never reachable, so the comment round-trip was untested.

## PostgreSQL reference

- `dumpStatisticsExt` → `pg_get_statisticsobjdef(oid)` for the DDL +
  `dumpComment(... statsextinfo->dobj ...)` for the comment.
- `collectComments` populates the shared comment set from `pg_description`;
  `dumpComment` matches on `(classoid, objoid)` and renders
  `COMMENT ON STATISTICS <name> IS <literal>;`. The object name is
  schema-qualified because `pg_dump` runs with an empty `search_path`.

## Change

**Test-only.** No engine change was required — the parser, executor, and
`pg_description` paths were already correct; this slice makes the now-reachable
paths *guarded*.

- `internal/testport/pgdump_connsetup_test.go`:
  - Setup: added `COMMENT ON STATISTICS public.statext_all IS 'a statistics
    comment'` to the `miscComments` fixture batch (the `statext_all` object is
    created by slice 314).
  - Assertions: added the missing slice-314 guards for both CREATE STATISTICS
    forms —
    `CREATE STATISTICS public.statext_all ON a, b FROM public.statext_t;`
    (default = all kinds, no kinds clause) and
    `CREATE STATISTICS public.statext_nd (ndistinct) ON b, c FROM
    public.statext_t;` (explicit single-kind) — plus the slice-315 guard
    `COMMENT ON STATISTICS public.statext_all IS 'a statistics comment';`.

## Verification

`go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS (4.3 s)
— all three new assertion strings appear in the real `pg_dump` 18.3 output
byte-for-byte. pgbench TPC-B smoke runs via the pre-commit hook.

## Follow-ups (next pg_dump getter-battery gaps)

- `ALTER STATISTICS … SET STATISTICS n` (`stxstattarget`) — `dumpStatisticsExt`
  emits this as a second statement when the target is non-default; goopg has no
  `ALTER STATISTICS` support yet.
- Expression extended statistics (`CREATE STATISTICS … ON (a+b) FROM t`) — needs
  an AST deparser; `StatisticsObject.HasExpr` already flags + omits it.
