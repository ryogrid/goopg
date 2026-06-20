Task: DU-002 slice 290 (loop #58) — COMPLETE, committing + pushing.

Last landed: function-call generation expression `upper(fn)` round-trips end-to-end on the
pg_dump oracle. The FIRST generation slice whose body is a function invocation (283–289 used
`*`/`+`/`||` or parenthesised arithmetic). Slice 289 already landed the production deparse fix
(`joinGeneratedExprTokens`, parser/ddl.go) and unit-tested its function-call branch; this slice
exercises it END-TO-END vs real pg_dump 18.3. TEST-ONLY — no production change.

goopg stores the generation source via joinGeneratedExprTokens (call parens TIGHT: `upper(fn)`,
not `upper ( fn )`); real pg_dump reads it back through pg_get_expr verbatim and emits inline
`fu text GENERATED ALWAYS AS (upper(fn)) STORED`. No rows inserted → dump-time deparse path only
(materialization of upper() not exercised). Render path identical to 283–289 (attgenerated 's' →
attrdefs[].separate=false pg_dump.c:9507; ispartition → shouldPrintColumn every column).

Files:
- internal/testport/pgdump_connsetup_test.go — pgfx fixture (CREATE TABLE pgfx +
  pgfx_1 PARTITION OF, after pgpp_1 fixture ~line 1632) + assertion (pgfx_1 block, after
  pgpp_1 assertion ~line 4684). TEST-ONLY.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 290 section + Next (291) note.
- .ralph/fix_plan.md — slice 290 progress (loop #58).

Gates: gofmt clean; go vet clean; TestPort_PgDumpConnectionSetup PASS (3.96s vs real pg_dump
18.3); pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 291+): a two-argument function-call generation expr (`coalesce(a, b)` / `concat(a, b)`)
to pin the `, `-separated argument-list branch of joinGeneratedExprTokens end-to-end on the oracle.
OR a multi-column / NULL-typed DEFAULT variant on the partition-leaf ALTER path.
