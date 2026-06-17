(idle — nothing in flight)

Last landed: DU-002 slice 149 (loop #114) — non-default volatility/strict now
round-trips through pg_dump. Slice 148 asserted only `add_one` (all-default
attrs), so the `pg_proc` virtual view's `provolatile`/`proisstrict` cells were
only ever exercised at default 'v'/'f'. This slice adds
`public.add_two(integer) … IMMUTABLE STRICT` and asserts pg_dump emits the exact
one-line `LANGUAGE sql IMMUTABLE STRICT` / `AS $_$ SELECT $1 + 2 $_$;` fragment.
dumpFunc (pg_dump.c:13531 / :13542) appends ` IMMUTABLE` when provolatile[0] !=
'v' and ` STRICT` when proisstrict[0] == 't', both inline after `LANGUAGE sql`.
goopg's CREATE FUNCTION executor already stores r.Volatile='i' / r.Strict=true
(parser defaults Volatile to 'v', overrides to 'i' for IMMUTABLE) and the view
emits provolatile='i' (text) + proisstrict='t' (bool) verbatim → dump matched on
the FIRST run. Clean positive test, NO production change (test + design-doc only).

NOTE (dead-code finding, not fixed): inferSQLFunctionVolatility
(operators_ddl.go:9938) is effectively unreachable — the parser defaults
stmt.Volatile to "v" (function.go:98), so `volatile := s.Volatile` is never "",
and the inference branch (`if volatile == ""`) never runs. Harmless; left as-is.

Key symbols: registerPgProcView (pg_proc_view.go), dumpFunc (pg_dump.c),
catalog.Routine.Volatile/Strict, parser CREATE FUNCTION attr clause.
Files: internal/testport/pgdump_connsetup_test.go (slice 149: create add_two +
2 assertions), docs/design/0110-0001-pg-dump-tap-port.md (Slice 149 section),
.ralph/fix_plan.md (loop #113/#114 PROGRESS).
Verified: gofmt OK; go build ./internal/... OK; TestPort_PgDumpConnectionSetup
PASS (2.16s, not skipped). ralph-state-guard pending.

Next direction (slice 150): a fresh pg_dump catalog-surface gap. Candidates:
a SECURITY DEFINER / LEAKPROOF / PARALLEL SAFE function (exercise the remaining
dumpFunc clauses — note proparallel column is always 'u' in the view, so PARALLEL
SAFE would NOT round-trip yet → a real divergence to fix); a set-returning
function's ROWS clause; or a CREATE PROCEDURE (prokind='p') round-trip.
