Task: DU-002 slice 292 (loop #60) — COMPLETE, committing + pushing.

Last landed: a NESTED function-call generation expression `upper(coalesce(gn, hn))` round-trips
end-to-end on the pg_dump oracle. Slices 290/291 pinned the single- and two-argument call-paren
branches of joinGeneratedExprTokens at ONE nesting level; this slice pins their COMPOSITION — a
call whose argument is itself a call — proving the helper keeps BOTH call parens tight while spacing
only the inner argument comma (`upper(coalesce(gn, hn))`, not `upper ( coalesce ( gn ,hn ) )`). The
`(`-after-ident and `)`-always-tight rules are depth-agnostic, so production already handled it;
this slice is the oracle round-trip proof. TEST-ONLY — no production change.

Because the test dumps a LIVE goopg server, pg_dump reads goopg's STORED source verbatim — both
lowercase function names are preserved (NO real-PG pg_get_expr case normalization in this path).

Render path identical to 281–291 (attgenerated 's' → attrdefs[].separate=false pg_dump.c:9507;
ispartition → shouldPrintColumn every column); 3 columns in attnum order (gn, hn, generated jn).
No rows inserted → dump-time deparse path only.

Files:
- internal/testport/pgdump_connsetup_test.go — pgnc fixture (CREATE TABLE pgnc + pgnc_1
  PARTITION OF, after pgcl fixture ~line 1674) + assertion (pgnc_1 block, after pgcl
  assertion ~line 4798). TEST-ONLY.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 292 section + Next (293) note.
- .ralph/fix_plan.md — slice 292 progress (loop #60).

Gates: gofmt clean; go vet clean; TestPort_PgDumpConnectionSetup PASS (3.90s vs real pg_dump
18.3); pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 293+): a THREE-argument function-call generation expr (`concat(a, b, c)`) to pin the
repeated-comma argument list end-to-end on the oracle. OR a multi-column / NULL-typed DEFAULT
variant on the partition-leaf ALTER path.
