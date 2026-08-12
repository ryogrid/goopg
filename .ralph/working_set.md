(idle — nothing in flight)

M0119-0006 48th slice landed — `COPY … FROM` (TEXT *and* CSV) of a zone-less
`timetz` field now takes the SESSION `TimeZone` instead of `+00`, so a table
loaded by COPY finally equals the same table loaded by INSERT. Item stays
UNCHECKED (standing slice-by-slice cluster); 1 ledger row flipped to
`resolved` (the 47th slice's COPY row), 2 new rows filed.

Selection note for the next loop: banner order re-verified against
`## Current Priority` (dated 2026-08-11). M-NIGHTLY filing done — no change,
`ci/logs/action-items.md` is still run `20260812-005501` with all four `## AI-`
items already filed. M0131's two unchecked items remain formally closed; M0130
has zero unchecked. **M0132 and M0133 both say "Priority: FILED, NOT PROMOTED"
— do not pick them up without a banner edit.** Fall-through lands on
M0119-0006 again.

Candidate 49th slices (cheapest first):
- **`COPY <t> TO STDOUT` of a `time`/`timetz` column hard-errors** (new ledger
  row, found E2E this loop) — `datumToCopyText` has no arm for either type, so
  COPY TO→COPY FROM is not a round trip. The fix is a package-placement call:
  reuse `pgdatetime.FormatTime`/`FormatTimeTZ` rather than adding a third copy
  of `time_out` (`appendTimeText` is stuck in `internal/server`).
- zone-less `timestamptz` COPY/literal reads as a UTC wall clock (new ledger
  row) — `tsZoneMode` has no "no zone field at all" arm; MOVES stored instants,
  so it needs a probe battery over `timestamp` (must not move) + `date`.
- `'10:00 EST.5'` / POSIX `tzparse()` port — closes TWO older ledger rows.
- `'20200101T040506'` — the run-together DATE half of `DecodeNumberField`.
- `box`/`int4range` amcheck key encodings; whole-DB unscoped pg_amcheck run.

Worth carrying:
- `bin/goopg` is NOT rebuilt by `scripts/tpch-spotcheck.sh` (that builds into
  its own path). An E2E run against a stale `bin/goopg` shows the OLD behaviour
  and reads exactly like a failed fix — `go build -o bin/goopg ./cmd/goopg`
  first. Cost this loop ~1 wasted server cycle.
- `psql` is at `$PWD/postgres/local_install/bin` (a symlink into the sibling
  checkout); `../postgres/...` does NOT resolve from the repo root.
- `bench/tpch/env.sh` must be sourced from the REPO ROOT and re-sourced in
  EVERY Bash call that runs the 65432 oracle — `PGPASSWORD` does not survive.
- `datumsFromCopyFields` is the ONE place the TEXT and CSV readers share; add
  GUC-dependent inputs there, not in either reader.

Gates: `go test ./internal/executor/ ./internal/config/ ./internal/pgdatetime/`
PASS, `RALPH_PRECOMMIT_SCOPE=units` PASS (initdb 299 s, rest cached),
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35), E2E on a capped throwaway
server (5533) matching the PG 18.3 oracle shape captured on 65432, pgbench
smoke via the commit hook.

In-flight: none.
