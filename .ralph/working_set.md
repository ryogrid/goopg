Task: DU-002 slice 291 (loop #59) — COMPLETE, committing + pushing.

Last landed: two-argument function-call generation expression `coalesce(cn, dn)` round-trips
end-to-end on the pg_dump oracle. The FIRST generation slice whose body is a multi-argument
function call (290 used single-arg `upper(fn)`). Pins the `, `-separated argument-list branch of
joinGeneratedExprTokens (comma TIGHT-left / SPACED-right) end-to-end vs real pg_dump 18.3. The
comma branch was already unit-tested (gen_override_test.go `coalesce(fn, gn)`); this slice proves
the oracle round-trip. TEST-ONLY — no production change.

Because the test dumps a LIVE goopg server, pg_dump reads goopg's STORED source verbatim — the
lowercase `coalesce` is preserved (NO real-PG pg_get_expr case normalization in this path). This
matters for picking future generation exprs: special SQL-construct nodes that real pg_get_expr
would uppercase/rewrite (COALESCE, CASE, IS NULL) still round-trip here because goopg returns the
stored token-joined source, not a re-deparsed canonical form.

Render path identical to 281–290 (attgenerated 's' → attrdefs[].separate=false pg_dump.c:9507;
ispartition → shouldPrintColumn every column); 3 columns in attnum order (cn, dn, generated en).
No rows inserted → dump-time deparse path only.

Files:
- internal/testport/pgdump_connsetup_test.go — pgcl fixture (CREATE TABLE pgcl + pgcl_1
  PARTITION OF, after pgfx_1 fixture ~line 1650) + assertion (pgcl_1 block, after pgfx_1
  assertion ~line 4736). TEST-ONLY.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 291 section + Next (292) note.
- .ralph/fix_plan.md — slice 291 progress (loop #59).

Gates: gofmt clean; go vet clean; TestPort_PgDumpConnectionSetup PASS (3.83s vs real pg_dump
18.3); pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 292+): a THREE-argument or nested-call generation expr (`concat(a, b, c)` /
`upper(coalesce(a, b))`) to pin repeated-comma / nested call-paren composition end-to-end on the
oracle. OR a multi-column / NULL-typed DEFAULT variant on the partition-leaf ALTER path.
