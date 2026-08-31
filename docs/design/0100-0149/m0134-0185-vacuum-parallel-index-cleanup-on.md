# M0134-0185 — `vacuum_parallel.sql`: `INDEX_CLEANUP ON` grammar gap, CLOSED

Status: **CLOSED** 2026-09-01. Sized live for the first time (CSV was
`not-tried`); one narrow parser fix took the case from 0/1 PASS to 100%
parity, and the stale `regressExcluded` policy entry blocking it from ever
running under `TestPort_RegressSuite` was reversed alongside (same pattern
as M0134-0184/unicode.sql).

## What the file tests

`postgres/src/test/regress/sql/vacuum_parallel.sql` (47 lines) is the
regression test for bug #17245: it builds a table with three indexes sized
so that parallel VACUUM's cost model routes two of them through parallel
workers and leaves the third (`vacuum_in_leader_small_index`, deduplicated
below `min_parallel_index_scan_size`) to the leader, then runs `VACUUM
(PARALLEL 4, INDEX_CLEANUP ON) parallel_vacuum_table` and re-inserts to
confirm no assertion failure. Critically, the expected output asserts
nothing about *real* parallel execution actually happening — no worker
count, no `pg_stat_activity` check — only that the two `SELECT` probes
return `t` / `2` (computed from `pg_relation_size`/`pg_size_bytes`, both
already supported) and that the `VACUUM` and following `INSERT` succeed
without error. A single-threaded `VACUUM` that treats `PARALLEL 4` as a
no-op satisfies the file exactly.

## Why it was excluded despite not needing real parallelism

`vacuum_parallel` was listed in `regressExcluded` (`internal/testport/
regress_suite_test.go`, mirrored in `cmd/gen-regress-coverage/main.go`)
under the "Parallel" bucket alongside `select_parallel` and
`write_parallel`, tagged "Parallel VACUUM; out of scope for goopg v0." —
a blanket policy call made without sizing the file's actual assertions.
Unlike `select_parallel` (M0134-0008, still PARKED: asserts
`pg_stat_database.parallel_workers_launched`, which needs a real
parallel-worker execution path goopg does not have), this file's own
comment says "Verify (**as best we can**)" — PG's authors already knew the
file can't strongly assert real parallelism from SQL, so it settles for
sizing predicates. That makes it reachable without any parallel-execution
engine work.

## Sizing (this loop, 2026-09-01)

`scripts/pg-regress-runner.sh -v vacuum_parallel`: **0/1 PASS, 16 diff
lines** (first live run). Single break:

```
VACUUM (PARALLEL 4, INDEX_CLEANUP ON) parallel_vacuum_table;
ERROR:  syntax error at or near "ON"
LINE 1: VACUUM (PARALLEL 4, INDEX_CLEANUP ON) parallel_vacuum_table;
  ^
```

### Root cause

PG's `opt_boolean_or_string` (`gram.y:1828`) accepts `TRUE_P` / `FALSE_P` /
the bare `ON` keyword / `NonReservedWord_or_Sconst` — `ON` gets its own
alternative because it is a fully reserved keyword and therefore cannot
also reduce through `NonReservedWord`/`ColId`, unlike its pair `OFF` (which
has no dedicated reserved token at all and lexes as a plain identifier).
goopg's mirror of this (`opt_opt_value` in `grammar/goopg_ext.y`, feeding
`vacuum_opt: vacuum_opt_name opt_opt_value`) had `TRUE_P` / `FALSE_P` /
`ColId` / `SCONST` / `signed_iconst` but no `ON` alternative, so
`INDEX_CLEANUP ON` inside a parenthesised `VACUUM (...)` option list never
reduced.

A second, latent gap surfaced while fixing the first: `vacuumNamedOpt`
(`internal/parser/support.go`) already special-cased `INDEX_CLEANUP`'s
three-valued semantics (`true` → force cleanup, `false` → suppress, absent
→ leave `auto`), but its `switch strings.ToLower(val)` only matched the
literal words `"true"`/`"false"` — the `off` spelling (which *did* already
parse, via `ColId`) silently fell through to the no-op default instead of
suppressing cleanup, and the new `on` spelling needed the same asymmetry
avoided from the start. Likewise `isFalseWord` (used by `truncate`/
`process_main`/`process_toast`) only matched `"false"`, not `"off"`.

## What landed

- `grammar/goopg_ext.y`: `opt_opt_value` gained an `ON { $$ = "on" }`
  alternative, directly mirroring PG's `opt_boolean_or_string`. Conflict
  pin unchanged at 60 (`ON` was already in the Makefile's known
  shift/reduce set from the unrelated JOIN-`ON` conflicts, so no new
  conflict was introduced or needed re-pinning).
- `internal/parser/support.go`: `vacuumNamedOpt`'s `index_cleanup` case
  now matches `"true"`/`"on"` → force, `"false"`/`"off"` → suppress.
  `isFalseWord` now matches `"false"`/`"off"` case-insensitively (was
  `"false"` only), fixing `TRUNCATE OFF`/`PROCESS_MAIN OFF`/
  `PROCESS_TOAST OFF` the same way.
- `internal/testport/regress_suite_test.go` +
  `cmd/gen-regress-coverage/main.go`: removed the stale `vacuum_parallel`
  entry from `regressExcluded` (both copies, kept in sync as required)
  now that the case is reachable and passes without any parallel-execution
  engine.
- New test `TestParseVacuumIndexCleanupOnOff`
  (`internal/parser/parser_test.go`): six cases covering `ON`/`OFF`/
  `true`/`false`/`auto`(no-op)/`ON` combined with `PARALLEL 4`, pinning
  `ForceIndexCleanup`/`NoIndexCleanup` on the parsed `*VacuumStmt`.

## Result

`scripts/pg-regress-runner.sh -v vacuum_parallel`: **1/1 PASS, 100.0%
parity, 0 diff lines.** CSV flipped `not-tried` → `pass`,
`pass_required=yes`, rationale points at
`TestPort_RegressSuite/vacuum_parallel`.

No deferral ledger row — the fix is complete and matches PG's real grammar
(`opt_boolean_or_string`) exactly; nothing about this case was shortcut.

## Gates run

- `scripts/pg-regress-runner.sh -v vacuum_parallel` (before/after: 0/1 →
  1/1, 100% parity).
- `make gen-parser` — clean, conflict count unchanged at 60.
- `go build ./...` clean.
- `go test ./internal/parser/...` — full package PASS (includes new
  `TestParseVacuumIndexCleanupOnOff`).
- `go test ./internal/executor/...` — full package PASS.
- `go test -v -run '^TestPort_RegressSuite$/vacuum_parallel$'
  ./internal/testport/` PASS.
- `make check-testport-inventory` PASS.
- `make regen-testport` — clean regen (CSV status flip + derived docs; only
  `vacuum_parallel`'s row and its own suite counts moved).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS.
- pre-commit pgbench smoke PASS (mandatory on every commit regardless of
  files changed).
- `make ralph-state-guard` PASS.
