# Working set — M0134-0027 PARKED (copy.sql), legacy CSV/BINARY COPY fix shipped

**Task:** M0134-0027 (`copy.sql`) — **PARKED** (case still FAILS; CSV row stays
`failed`, no `make regen-testport`).

**Re-run at HEAD confirmed not stale:** 364 diff lines / 21 `^+ERROR` / 15
`^-ERROR`. Also re-ran under the PG-parity env (`PGTZ`/`PGDATESTYLE`/
`PGOPTIONS` intervalstyle/`LC_MESSAGES=C`, per the M0134-0026 harness-gap
lesson) — **byte-identical to default**, so this case is NOT a harness false
negative (recorded as a negative-result deferral row so this isn't re-checked).

**What shipped:** `validateCopyOptions` (`internal/optimizer/copy.go`) now
accepts the legacy pre-9.0 bareword `COPY ... CSV` / `COPY ... BINARY` trail.
The parser (`internal/parser/copy.go:311`, `parseCopyLegacyTrail`) already
emitted `CopyOption{Name:"csv"/"binary", Bool:true}` and the executor already
consumed that exact shape (`internal/executor/copy.go:24`,
`copy_csv.go:46`) — only the optimizer's validator switch had no case for it,
so every legacy-syntax COPY was rejected with `42601 option not recognized`
before reaching either already-compatible end. Fix: two new switch cases
sharing the `formatSpecified` guard with the existing `case "format"`, mirrors
PG's grammar (`gram.y` `copy_opt_item`: BINARY/CSV both produce
`makeDefElem("format", ...)`, so a real duplicate is caught by ONE guard).
364 -> 334 diff lines (21 -> 19 `^+ERROR`). Unit test
`TestPlanCopyLegacyTrailFormat` added in `internal/optimizer/copy_test.go`.

**Two deferral rows appended** (2026-08-20, M0134-0027): the ranked remaining
bucket breakdown (file-based `COPY ... TO 'file'` unsupported ~35 lines is
next-largest, then `HEADER MATCH` validation ~31, lone `\.` marker detection
~16, `WHERE (...)` clause on COPY FROM file ~7, `COPY DEFAULT` option ~7, misc
tail ~13) with resume points; and the negative PG-parity-env result.

**Next step:** select **M0134-0028 (`horology.sql`)** — re-read the fix_plan
banner first (sole ordering authority). CSV status is `failed`. Apply the
standing rule: re-run `scripts/pg-regress-runner.sh --verbose horology` at
HEAD FIRST (this is a datetime-heavy case — near-certain to need the
PG-parity env per the M0134-0026 lesson; run BOTH ways and compare before
sizing). Then interrogate the park verdict once, as always.

**Gates run:** `go build ./...` PASS; `go test ./internal/optimizer/...
./internal/executor/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS; `scripts/pg-regress-runner.sh
--verbose copy` before/after 364->334 confirmed; pre-commit pgbench smoke PASS
on both commits (375/691/12841 tps last run); `make ralph-state-guard`
INCONSISTENT -> auto-REPAIRED -> OK (progress.json completed-marker was the
prior loop's clean-exit marker, reconciled to in_progress; committed
separately).

**Delegation:** `tmp/ralph-handoffs/M0134-0027a` (researcher sizing, DONE;
implementer, 1 round, DONE — report captured by coordinator, worker tool
policy blocked its own report.md write again, same as M0134-0026b).
**In-flight:** none.
