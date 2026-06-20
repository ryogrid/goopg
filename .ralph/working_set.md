Task: DU-002 slice 288 (loop #56) — COMPLETE, committed + pushed.

Last landed: a TEXT generated column over the `||` string-concatenation operator inherited
onto a partition leaf round-trips. Every prior generation slice (283–287) used an `integer`
column over `+`/`*` arithmetic; 288 uses `text` over `||` — proving the inherited-leaf
generation render path is BOTH type-agnostic and operator-agnostic. Render path keys ONLY off
attgenerated ('s', via attGeneratedFor at pg18_user_catalog_rows.go:834 — inspects no column
type) and the verbatim pg_get_expr pass-through of the stored generation source. Same
attrdefs[].separate=false (pg_dump.c:9507) + ispartition shouldPrintColumn (281/282) path as
283–287. Leaf body prints in attnum order: `ca text`, `cb text`, inline `cc text GENERATED
ALWAYS AS (ca || cb) STORED`. The `||` token joins flat (no parens/func call) so deparse stays
faithful: pg_dump wraps `(%s)` → `(ca || cb)`. NO production change.

Fixture: `CREATE TABLE public.pgcc (ca text, cb text, cc text GENERATED ALWAYS AS (ca || cb)
STORED) PARTITION BY LIST (ca)` + `CREATE TABLE public.pgcc_1 PARTITION OF public.pgcc FOR
VALUES IN ('x')`.
Asserted: pgcc_1 block prints `ca text` BEFORE `cb text` BEFORE inline `cc text GENERATED
ALWAYS AS (ca || cb) STORED` (two strings.Index ordering checks); `ATTACH PARTITION
public.pgcc_1 FOR VALUES IN ('x')` survives.

KEY DISCOVERY (gates slice 289): a FuncExpr generation expr (`upper(name)`) is NOT a pure
test slice. The parser stores GeneratedExpr as space-joined tokens (ddl.go:2607), so
`upper(fn)` becomes `upper ( fn )` — mismatches real pg_dump's `upper(fn)`. It needs
expression canonicalization (a PRODUCTION change). Parens also break (`(a+b)*2` → `( a + b ) * 2`).
Only flat operator chains (`a + b`, `a * 2`, `a || b`) stay faithful with verbatim text.

Files:
- internal/testport/pgdump_connsetup_test.go — pgcc fixture (after pgmx_1) + assertion block
  (after pgmx_1 ATTACH assertion).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 288 section + Next (289) note.
- .ralph/fix_plan.md — slice 288 progress (loop #56).

Gates: gofmt clean; go vet clean; TestPort_PgDumpConnectionSetup PASS (3.84s, vs real
pg_dump 18.3); pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 289+): the FuncExpr generation deparse PRODUCTION slice — canonicalize
GeneratedExpr so a parsed-then-deparsed function call renders without token-join spaces
(`upper(fn)`, not `upper ( fn )`). OR a multi-column / NULL-typed DEFAULT variant on the
partition-leaf ALTER path.
