Task: DU-002 slice 293 (loop #61) — COMPLETE, committing + pushing.

Last landed: a THREE-argument function-call generation expression `concat(ka, la, ma)` round-trips
end-to-end on the pg_dump oracle. Slice 291 pinned the two-argument call-paren branch of
joinGeneratedExprTokens (ONE comma); this slice extends to a REPEATED-comma argument list, proving
the helper emits `, ` between EVERY adjacent argument pair while keeping the single call paren tight
(`concat(ka, la, ma)`, not `concat(ka, la,ma)` or `concat(ka ,la ,ma)`). The comma-spacing rule
fires TWICE in one call; count-agnostic in production, so this is the oracle round-trip proof.
TEST-ONLY — no production change.

Table named `pg3c` (NOT pgcc — pgcc already used by slice 288's `||` concat fixture). Because the
test dumps a LIVE goopg server, pg_dump reads goopg's stored source verbatim — lowercase function
name preserved (no real-PG pg_get_expr case normalization).

Render path identical to 281–292 (attgenerated 's' → attrdefs[].separate=false pg_dump.c:9507;
ispartition → shouldPrintColumn every column); 4 columns in attnum order (ka, la, ma, generated na).
No rows inserted → dump-time deparse path only.

Files:
- internal/testport/pgdump_connsetup_test.go — pg3c fixture (CREATE TABLE pg3c + pg3c_1
  PARTITION OF, after pgnc fixture ~line 1721) + assertion (pg3c_1 block, after pgnc
  assertion ~line 4893). TEST-ONLY.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 293 section + Next (294) note.
- .ralph/fix_plan.md — slice 293 progress (loop #61).

Gates: gofmt clean; go vet clean; TestPort_PgDumpConnectionSetup PASS (3.44s vs real pg_dump
18.3); pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 294+): a function-call generation expr with a LITERAL argument (`concat(ka, '-', la)`)
to pin string-literal token rendering inside an argument list. OR a multi-column / NULL-typed
DEFAULT variant on the partition-leaf ALTER path.
