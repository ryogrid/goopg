# EX1-04 Cut 0 — alloc arm on the P4-01 flip (measurement-only, no executor change)

```
label: EX1-04-cut0 | date: 2026-09-04
pre:  tmp/goopg-p401s2  (P4-01 slice 2, pre-flip)
post: tmp/goopg-p401s3b (P4-01 slice 3 = commit 1d804ae02)
suite: TPC-H SF=1 Q9 | regime: stats=S-cold, serial
  (max_parallel_workers_per_gather=0), work_mem 64MB
GOGC/GOMEMLIMIT: 100/12GiB | cgroup scopes cut0pre/cut0b 20G/24G/0
fresh server per arm | port 65433 tpch@tpch
alloc: pprof heap alloc_space -base before/after (endpoint verified
  owned by the measured server PID — a stale :6060 holder burned the
  first attempt, profiles were byte-identical lifetime accumulation)
```

| | pre-flip | post-flip |
|---|---|---|
| Q9 serial | 20.14 s | 13.88 s |
| alloc window (alloc_space delta) | 9.43 GB | 8.52 GB (−10%) |
| top bucket `executor.init.5.func1` | 4.21 GB | 3.54 GB |
| `ownedBuildRow` (EX1-04's target) | — | 0.06 GB |
| Q9 values hash | `6bbb80a6…` | `6bbb80a6…` (identical) |

Timing neutral-or-better ✓, alloc down ✓ — half of EX1-04's goal is
met by the planner flip alone, no executor risk. (Fresh-server times
are slower than the 10.80 s slice-3 gate number: that server had a
warm page cache from the values sweep. Same-instrument A/B is what is
protocol-fair here.)

Per the unblock review: hash-build half needs-design-revision (Cut 1
test-only next), sort half still blocked on deferred upper-target
slice (c).
