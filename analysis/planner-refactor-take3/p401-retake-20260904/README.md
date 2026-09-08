# P4-01 §12 re-take on HEAD (pre-code step, take3 TODO.md:503-504)

Re-takes take2 `impl/P4-A-pathtarget.md` §12 (rev 4, 2026-09-03) after
eleven optimizer commits, incl. two plan-affecting flips
(`82dd30bbc` collapse default-ON, `00d56df90` NARROW_BUILD
default-ON). PG side not re-captured (reference cluster untouched —
rev-4 PG numbers stand).

```
label: P4-01-retake | date: 2026-09-04
goopg: 588aa5fb5 (P4-01 slice 1, behaviour-neutral) +dirty
suite: TPC-H SF=1 Q9 | regime: stats=S-cold, serial
  (max_parallel_workers_per_gather=0)
GOGC/GOMEMLIMIT: 100/12GiB | cgroup scope p401rt 20G/24G/0
work_mem/effective_cache_size: 64MB/2GB | port 65433 tpch@tpch
```

TPC-H Q9, SF=1, `work_mem` 64 MB:

| | PostgreSQL 18.3 (rev 4) | goopg rev 4 (09-03) | goopg HEAD (09-04) |
|---|---|---|---|
| rows through join tree | ~319 k | 321,056 | 321,056 |
| tuple widths | 23 / 32 / 54 / 81 B | 1098 / 1642 / 2090 / 3164 B | 1096 / 896 / 896 / 710 B |
| peak hash memory | 38 MB | 97 MB | 91.6 MB (witness build) |
| batches (witness) | 1 throughout | 8 | **2** |
| Q9 serial | 6.2 s | 63.8 s | **14.7 s** |

Movement since rev 4 is NARROW_BUILD default-ON (+ executor EX1
narrowing on the scan side): two join levels dropped 1642→896 and
2090→896, batches 8→2, time 63.8→14.7 s. The remaining gap to PG
(widths ~10×, batches 2 vs 1, 14.7 vs 6.2 s) is Slice 2+'s working
surface. The Slice-2 gate stays in MODEL currency (`NBatch` 2→1);
runtime `Batches: 2` is the new BEFORE.

Full plan: `/tmp/opencode/p401-q9plan.txt` (run-local; table above is
the committed record). Values: Q9 ALGERIA rows match the EX0-05
record.
