# Autovacuum: honor `autovacuum_enabled` reloption (M0122-0016)

status: accepted
date: 2026-07-08
supersedes: none

## Source

`unimplemented_feat.json` task `M0086` (deferred 2026-05-11, commit `606cff26`):
"The `needsVacuum` function always returns true when `RowCount > 0` and does
not respect autovacuum reloptions like `autovacuum_enabled = off`." Filed as
its own M0122 task per the fix_plan note that small residual entries not
matching an existing M0122 cluster get a fresh number.

## Problem

`catalog.Table` has carried `AutovacuumEnabled`/`AutovacuumEnabledSet` (the
table's `WITH (autovacuum_enabled=BOOL)` storage parameter) since M0110-0001
(DU-002 slice 196), but the field was catalog/dump-only: `pg_dump`/`pg_class.
reloptions` round-tripped it, yet `internal/autovacuum.Launcher.needsVacuum`/
`needsAnalyze` never read it. A user setting `autovacuum_enabled=false` on a
table got no behavioral effect — the launcher kept vacuuming/analyzing it on
the normal schedule.

## Upstream reference

`postgres/src/backend/postmaster/autovacuum.c`,
`relation_needs_vacanalyze()` (~line 3054):

```c
av_enabled = (relopts ? relopts->enabled : true);
...
/* User disabled it in pg_class.reloptions?  (But ignore if at risk) */
if (!av_enabled && !force_vacuum)
{
    *doanalyze = false;
    *dovacuum = false;
    return;
}
```

Two invariants to preserve:
1. `autovacuum_enabled=false` suppresses **both** vacuum and analyze for that
   relation.
2. Anti-wraparound forcing (`force_vacuum`, goopg's existing `RelFrozenXID`/
   `autovacuumFreezeMaxAge` check) overrides the disable — a table can't opt
   out of wraparound protection.

## Change

`internal/autovacuum/launcher.go`:
- `needsVacuum`: after the existing anti-wraparound check (unchanged,
  already returns `true` first when at risk), add
  `if tbl.AutovacuumEnabledSet && !tbl.AutovacuumEnabled { return false }`
  before the row-count heuristic.
- `needsAnalyze`: add the identical gate before its (stub) `return true`.

No catalog/planner/parser change — `AutovacuumEnabledSet`/`AutovacuumEnabled`
already exist and are already populated by `ApplyTableReloptions` from `WITH
(autovacuum_enabled=...)` and restart-replay. This is purely wiring the
existing catalog field into the one runtime consumer that ignored it.

`internal/catalog/catalog.go`'s `AutovacuumEnabled` field doc comment updated
(was: "goopg has no autovacuum, so the value is catalog/dump-only (advisory;
runtime unaffected)" — stale since `internal/autovacuum` has existed since
M0019).

## Non-goals

- Threshold-based reloptions (`autovacuum_vacuum_scale_factor`,
  `autovacuum_vacuum_threshold`, etc.) already exist on `catalog.Table` as
  catalog/dump-only fields and remain so — `needsVacuum`'s heuristic is
  `RowCount > 0`, not a real dead-tuple-fraction computation, so wiring
  per-table scale factors in has no consumer yet. Out of scope for this task.
- `needsAnalyze` remains a stub (`return true` when enabled) — real
  threshold-based analyze scheduling is a separate, larger feature.

## Tests

`internal/autovacuum/launcher_test.go`:
- `TestNeedsVacuumRespectsAutovacuumEnabledReloption` — unset (default
  true), explicit `false`, explicit `true`, for both `needsVacuum` and
  `needsAnalyze`.
- `TestNeedsVacuumAntiWraparoundOverridesDisabledReloption` — `RelFrozenXID`
  aged past `autovacuumFreezeMaxAge` with `autovacuum_enabled=false` still
  forces `needsVacuum() == true`.

Confirmed non-vacuous via `git stash` on `launcher.go` alone (the first test
fails with the pre-fix "always true" behavior; the anti-wraparound test
passes either way, confirming it isn't accidentally the thing gating the
first test).

## Gates run

- `go build ./...` clean.
- `go test ./internal/autovacuum/...` PASS (3/3, including the two new
  tests).
- `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS (0
  failed transactions, standard/simple-update/select-only).
