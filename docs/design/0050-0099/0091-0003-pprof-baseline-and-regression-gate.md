# Design 0091-0003 — pprof baseline + manual regression gate

**Status:** authoritative for M0091-0005 implementation.
**Milestone:** [M0091](../../milestones/0091-select-only-tps-regression-recovery.md).

## Problem

The 17× select-only TPS regression between the post-M0026
baseline (~6,400 TPS @ -c 4) and the 2026-05-11 measurement
(350.89 TPS @ -c 10) accumulated across many unrelated
milestones (M0073 arena Datum; M0077 4-slice planner; M0080
VM/FSM persistence; etc.). No single milestone caused it;
each added a marginal cost that wasn't pprof-checked against
a baseline.

To prevent recurrence, M0091's recovery work captures a
post-fix pprof baseline AND documents the manual comparison
idiom so a future regression of this magnitude is caught at
the next pgbench measurement rather than discovered by
end-user observation.

## Approach

After M0091-0001 + M0091-0002 land and the
M0091-0003 re-measurement clears the ≥ 1,000 TPS bar,
archive the post-fix `cpu.prof` + `heap.prof` + `allocs.prof`
under `pprof-data/baseline/select-only-c10/` (local-only
location; `pprof-data/` is gitignored).

Document the comparison idiom in this design doc + reference
it from `analysis/oltp-performance/wal-bottleneck.md`:

```sh
# Capture a fresh post-fix profile during a pgbench run
pgbench -h 127.0.0.1 -p 5433 -U postgres -c 10 -j 10 -T 60 -S postgres &
curl -s -o /tmp/new.cpu.prof "http://127.0.0.1:6060/debug/pprof/profile?seconds=30"
curl -s -o /tmp/new.allocs.prof "http://127.0.0.1:6060/debug/pprof/allocs"
wait

# Diff against the baseline
go tool pprof -base pprof-data/baseline/select-only-c10/cpu.prof \
    /tmp/new.cpu.prof
go tool pprof -base pprof-data/baseline/select-only-c10/allocs.prof \
    /tmp/new.allocs.prof
```

`-base` shows the diff: positive flat = new code is slower /
allocates more; negative = improvement. A new milestone that
adds > 10 % CPU or allocations to a top-5 hot function would
show prominently.

## Out of scope

- CI integration (running pgbench + pprof comparison on every
  PR): too expensive for the value at goopg's current
  development scale. The manual idiom is sufficient.
- Automated regression-rule extraction (e.g., "GC CPU share
  must stay < 30 %"): manual judgment based on the diff is
  adequate.

## Status — 2026-05-11

**Baseline archived.** The post-M0091 cpu/heap/allocs profiles
are now at `pprof-data/baseline/select-only-c10/` (local-only;
`pprof-data/` is gitignored). README inside that directory
records capture conditions (commit hash, pgbench params,
goopg postgresql.conf, host). Use `go tool pprof -base ...`
as documented above for diff visualisation.

The baseline is NOT updated by M0092 (which regressed
slightly at this workload, see
`bench/pgbench-compare/results/20260511_goopg_select-only_m0092_summary.md`).
Future milestones that deliver a measurable improvement
should refresh the baseline AND update this design doc with
the new commit hash.

## File layout

```
pprof-data/                                 # gitignored
└── baseline/
    └── select-only-c10/
        ├── cpu.prof                        # 30-s CPU profile
        ├── heap.prof                       # heap inuse_space
        ├── allocs.prof                     # cumulative allocs
        └── README.md                       # capture conditions
```

The baseline README records the goopg commit hash, the
pgbench parameters, and the goopg postgresql.conf settings
that produced the profiles. Without this metadata the baseline
is meaningless across architecture / config changes.
