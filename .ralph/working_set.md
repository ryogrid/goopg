(idle — nothing in flight)

M0119-0006 45th slice landed (CSV reader for `COPY … FROM`). Item stays
UNCHECKED (standing slice-by-slice cluster; 1 ledger row flipped to
`resolved`, 3 new rows filed).

Selection note for the next loop: banner order re-verified against
`## Current Priority` (dated 2026-08-11). **Two new milestones appeared —
M0132 (extended-protocol transactions) and M0133 (`information_schema` on
disk), both filed 2026-08-12 — and BOTH say "Priority: FILED, NOT PROMOTED"
in their own section: the banner is still the sole ordering authority and
still names M0131 first. Do not pick them up without a banner edit.**
M0131's two unchecked items remain formally closed (S9 closure bookkeeping →
successor M0133; S24 deferred-with-ledger); M0130 has zero unchecked; the
banner's list skips M0110 and names M0119 then M0122. M-NIGHTLY filing done —
`ci/logs/action-items.md` is still run `20260812-005501`, all four `## AI-`
items already filed. Fall-through lands on M0119-0006 again.

Worth carrying:
- **Two orphaned testport servers are running** (`go run ./cmd/goopg start -D
  /tmp/TestPort_RegressSuite27699556/001/data`, pids 851840/851923, ~2h old,
  ~65 MB). Not owned by this loop and the classifier denies killing
  non-owned PIDs — flagged for the user, not a blocker.
- `goopg initdb` does NOT exist; the subcommand is `goopg init -D <dir>`.
  A `for i in $(seq …); do … break; done` readiness loop followed by a bare
  `echo UP` prints UP even when every probe failed — put the echo inside the
  loop (cost ~5 min this loop).
- `internal/server` is NOT in `RALPH_PRECOMMIT_SCOPE=units` (the scope list
  jumps protocol → runtimeshim). Run `go test ./internal/server/` explicitly
  (~40 s) for any change there.
- `startTestServer` cannot test COPY (storage-less row-counting stub); use
  `startCopyExecServer` (`copy_executor_test.go`, table
  `items(id int4 NOT NULL, label text)`).
- `quote_nullable()` does not exist on goopg — use
  `coalesce(c,'<NULL>')` + `c IS NULL` when diffing NULL-shaped output
  against the oracle.
- Reference PG on 65432 needs `source bench/tpch/env.sh` (exports
  `PGPASSWORD`); a bare `psql -U postgres` hangs on the password prompt.

Gates: `go test ./internal/executor/ ./internal/server/` PASS (6.1 s / 39.8 s),
`RALPH_PRECOMMIT_SCOPE=units` PASS (warm cache), `scripts/tpch-spotcheck.sh`
PASS (Q12=2, Q13=35), pgbench smoke via the commit hook.

In-flight: none.
