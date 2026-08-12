(idle — nothing in flight)

M0119-0006 47th slice landed — a zone-less `timetz` now inherits the SESSION
`TimeZone` instead of `+00`. Item stays UNCHECKED (standing slice-by-slice
cluster; 2 new ledger rows filed, none flipped to `resolved`).

Selection note for the next loop: banner order re-verified against
`## Current Priority` (dated 2026-08-11). M-NIGHTLY filing done — no change,
`ci/logs/action-items.md` is still run `20260812-005501` with all four `## AI-`
items already filed. M0131's two unchecked items remain formally closed; M0130
has zero unchecked. **M0132 and M0133 both say "Priority: FILED, NOT PROMOTED"
— do not pick them up without a banner edit.** Fall-through lands on
M0119-0006 again.

Candidate 48th slices (cheapest first):
- **COPY FROM zone-less `timetz`** (new ledger row) — mechanical: thread the
  session zone through `DecodeCopyTextRow`/`datumsFromCopyFields`/
  `copyTextToDatum` (copy.go:72 already resolves it for the OUTPUT direction).
- `'10:00 EST.5'` / POSIX `tzparse()` port — now closes TWO ledger rows (the
  46th slice's zone token AND `SET TimeZone='+05:30'` falling back to UTC).
- `'20200101T040506'` — the run-together DATE half of `DecodeNumberField`.
- `box`/`int4range` amcheck key encodings; the whole-DB unscoped pg_amcheck run.
- timestamptz array elements render in UTC only.

Worth carrying (new this loop):
- `bench/tpch/env.sh` must be sourced from the REPO ROOT and re-sourced in EVERY
  Bash call that runs the 65432 oracle — `PGPASSWORD` does not survive between
  calls, and a bare `psql -U postgres` then blocks on a password prompt.
- `coerceRowForConstraintChecks` (operators_storage.go) is the INSERT/UPDATE
  type-coercion switch. Any input-function fix that depends on `*Context` is
  INVISIBLE to DML unless the type is listed there — the literal otherwise stays
  `KindString` until `encodeValuePG`, which has no ctx. It listed only
  int2/int4/int8/date/timestamp/timestamptz/numeric before this loop.
- `TypedStringLit.CachedTime` is keyed on the planner NODE, so any GUC-dependent
  literal arm must not fill it (one session's zone would leak into another's).
- `expr.go`'s cross-kind probe types a string as `timetz` iff `offsetSecs != 0`
  — it must keep the UTC default or every bare `'10:00'` gets mistyped.

Gates: `go test ./internal/executor/ ./internal/pgdatetime/ ./internal/config/
./internal/pgarray/ ./internal/planner/` PASS, `./internal/server/` PASS,
`RALPH_PRECOMMIT_SCOPE=units` PASS, `TestPort_RegressSuite` PASS (186 s),
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35), E2E vs PG 18.3 on 65432
byte-identical, pgbench smoke via the commit hook.

In-flight: none.
