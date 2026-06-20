Task: DU-002 slice 287 (loop #55) — COMPLETE, committed + pushed.

Last landed: a MID-POSITION generation expression mixing a BACKWARD Var, a literal, and a
FORWARD Var inherited onto a partition leaf round-trips. `mg` is attnum 2 in the parent and
references `ma` (attnum 1, backward) + literal `1` + `mc` (attnum 3, forward) — so one
generation deparse resolves a Var on EACH side plus a Const. Slices 285/286 each exercised a
single direction; 287 does both at once. Same render path as 283–286 (attgenerated forces
attrdefs[].separate=false, pg_dump.c:9507; ispartition forces shouldPrintColumn true for every
column, slices 281/282) → leaf body prints in attnum order: `ma integer`, inline
`mg integer GENERATED ALWAYS AS (ma + 1 + mc) STORED`, `mc integer`. goopg stores the
generation expr as verbatim source text and renders via pg_get_expr; pg_dump wraps `(%s)`, so
the three-operand expr prints FLAT (no nested parens), matching goopg's existing convention
(`a + b + 1000`). NO production change — composes name-based generation resolution
(evalGeneratedExpr over catalog.Column) with partition-leaf inheritance (281–286).

Fixture: `CREATE TABLE public.pgmx (ma integer, mg integer GENERATED ALWAYS AS (ma + 1 + mc)
STORED, mc integer) PARTITION BY LIST (ma)` + `CREATE TABLE public.pgmx_1 PARTITION OF
public.pgmx FOR VALUES IN (1)`.
Asserted: pgmx_1 block prints `ma integer` BEFORE inline `mg integer GENERATED ALWAYS AS
(ma + 1 + mc) STORED` BEFORE `mc integer` (two strings.Index ordering checks); `ATTACH
PARTITION public.pgmx_1 FOR VALUES IN (1)` survives.

Files:
- internal/testport/pgdump_connsetup_test.go — pgmx fixture (after pgfr_1) + assertion block
  (after pgfr_1 ATTACH assertion).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 287 section + Next (288) note.
- .ralph/fix_plan.md — slice 287 progress (loop #55).

Gates: gofmt clean; go vet clean; TestPort_PgDumpConnectionSetup PASS (3.81s, vs real
pg_dump 18.3); pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 288+): a multi-column / NULL-typed DEFAULT variant on the partition-leaf ALTER
path; OR a generated column whose expression applies a function call (e.g. `upper(name)`) —
a FuncExpr node in the inherited-leaf generation deparse.
