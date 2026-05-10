# Milestone 0086 — autovacuum needsVacuum PG-parity heuristics

**Status:** planned
**Depends on:** M0019 (autovacuum launcher), M0046-0005
(anti-wraparound)
**Drives:** PostgreSQL-compatible autovacuum trigger heuristics
so production workloads vacuum at the right cadence —
neither too aggressive (wasted I/O on quiescent tables) nor
too lazy (bloated heap + stale planner statistics).

## Context

`internal/autovacuum/launcher.go::needsVacuum` currently
returns `true` whenever `tbl.Stats.RowCount > 0` (plus the
anti-wraparound branch). `needsAnalyze` returns
unconditional `true`. The launcher's `MinVacuumAge` /
`MinAnalyzeAge` (default 5 min) is the only thing keeping
busy tables from getting vacuumed every tick.

PostgreSQL's trigger formula is
`threshold = base_threshold + scale_factor * reltuples`,
compared against `n_dead_tup` (vacuum) or
`n_mod_since_analyze` (analyze). Per-table `reloptions`
(`autovacuum_enabled`, `autovacuum_vacuum_threshold`,
`autovacuum_vacuum_scale_factor`,
`autovacuum_analyze_threshold`,
`autovacuum_analyze_scale_factor`) override the GUC
defaults.

## Required design docs

- `docs/design/0086-0001-autovacuum-trigger-heuristics.md`
  (dead-tuple + modified-tuple counters; GUC defaults;
  reltuples-scaled thresholds; cold-table handling).
- `docs/design/0086-0002-per-table-autovacuum-reloptions.md`
  (catalog representation for per-table autovacuum knobs;
  parser surface for `ALTER TABLE ... SET (...)`; precedence
  vs GUC defaults).

## Tasks

Tasks will be detailed when this milestone is picked up. See
the fix_plan.md note about the milestone-only format.

## Definition of Done (sketch)

- `needsVacuum` returns true only when dead-tuple counter
  exceeds `vacuum_threshold + vacuum_scale_factor *
  reltuples`, OR anti-wraparound forces it.
- `needsAnalyze` returns true only when modified-tuple
  counter exceeds the analyze-side equivalent.
- Per-table `autovacuum_enabled = off` is honoured.
- Per-table `autovacuum_vacuum_threshold` /
  `_scale_factor` (and analyze counterparts) override
  cluster-level GUC values.
- Existing M0046-0005 anti-wraparound trigger continues to
  fire regardless of per-table opt-out (PG behaviour).
