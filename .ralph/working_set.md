(idle — nothing in flight)

Loop #122 landed **M0131-S17** — goopg now stamps `pg_control.State =
DB_IN_PRODUCTION` at the end of `Open`, before any client is accepted. This
closed a LIVE data-loss bug: a SIGKILL inside the first `checkpoint_timeout`
(300 s) left the directory claiming `DB_SHUTDOWNED`, so a hosted PG took none
of the three `InRecovery` arms (`xlogrecovery.c:924-936`), skipped
`PerformWalRecovery()` entirely, and overwrote goopg's committed WAL tail with
no PANIC and nothing alarming logged.

Carry-forward:

- **The remaining "LAND FIRST" data-loss item is M0131-S16** (WAL reader treats
  rmids 16-21 as end-of-WAL → silent truncation → permanent on first append,
  design `0131-0013`). That is the cheapest high-value next pick unless the
  banner moves.
- **S18.1 is the natural follow-on to what just landed**: the new stamp rides
  `control.UpdateControlFile`, whose `os.WriteFile` is `O_TRUNC` + no fsync, so
  a crash mid-write leaves a zero-length `pg_control` and PG PANICs `read 0 of
  296`. Ledgered. S18 also carries the nine undecoded `checkPointCopy` fields
  and the hardcoded TLI 1 (which stomps a promoted cluster).
- **goopg still has NO reader of `pg_control.State`** (S17.3 probe, ledgered) —
  it cannot tell its own crashed directory from a clean one. That is S20.1. The
  fix_plan's "S15 changes that" pointer was stale; the reader is S20.
- The fix_plan's S17 row cited design `0131-0013`; the real doc is
  **`0131-0014`** (0131-0013 is the WAL-reader doc). Corrected in the check-off
  text; other Theme F rows may carry the same off-by-one — check before trusting
  a design filename in this milestone's rows.
- Nightly `20260811-014635` (12 items) was **already filed** by a prior loop at
  fix_plan.md:717 — do not re-file. 9 of them are `regress/*` "output mismatch;
  normalization rules need extension", the same recurring class as the
  20260809 batch.

Technique worth reusing: the guard was proven fail-when-broken by
short-circuiting `stampInProduction` with a temporary `if true { return nil }`
(file backed up to /tmp, restored, rebuild verified) — `got 1, want 6`. A green
new test on a startup path proves very little otherwise.

Gates run this loop: new `TestOpenStampsDBInProduction` PASS (and PASS-inverted
without the fix), `internal/initdb` PASS (65 s), `internal/control` +
`internal/wal` PASS, `TestE2E_{PGColdStartOnGoopgDataDir,GoopgColdStartOnPGDataDir}`
PASS (27 s), whole `^TestE2E_` family PASS, UNITS PASS, pgbench smoke via the
commit hook, `make ralph-state-guard` OK (auto-repaired the stale
completed-marker, as usual).

In-flight: none.
