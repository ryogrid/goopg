# TPC-H SF=1 warm-start query timings

Captured alongside the plan fixtures in `../plans-pg/` (PG) and
`../../../plan_snapshots/` (goopg) so a plan and the runtime it produced can be
read as a matched pair.

## Protocol

- **Warm start.** One full pass over all queries is run and **discarded**, then
  two measured passes follow on the same server. Both measured passes are
  recorded, not averaged: the spread between them is the honest error bar, and
  a reader who quotes one number without it is over-claiming.
- Serial per-query connections, `tmp/tpch-acceptance-runner`, 10 min per-query
  budget.
- goopg runs under the cgroup memory cap with `GOGC=100 GOMEMLIMIT=12GiB` and
  `GOOPG_ANALYZE_SEED` pinned.
- Both engines run the **same** measurement settings, aligned 2026-09-06 (see
  `../README.md`): `shared_buffers = 2048MB`, `work_mem = 64MB`,
  `effective_cache_size = 2GB`, `autovacuum = on`. Each file's header records
  what its engine actually reported, read back from the live server rather than
  from the config file.

## Reading these files

`secs are machine-specific.` The fixture is the **pair of passes and their
spread**, and the goopg-vs-PG ratio on one host — not the absolute seconds.

Server age is deliberately not held constant *between* the two measured passes
of one file: pass 2 runs on an older server than pass 1. That is visible as the
p1/p2 spread rather than hidden by restarting, because a goopg server that has
just run a heavy query sits at GOMEMLIMIT with GOGC=off and a restart would
mask GC behaviour that is part of what is being measured. For an A/B between
two builds, hold server age constant instead — see the sweep-tail-collapse note
in `../../../CLAUDE.md`.

## Files

| file | engine |
|---|---|
| `20260906-warm-goopg.txt` | goopg at the recorded commit |
| `20260906-warm-pg.txt` | PostgreSQL 18.3 reference |
