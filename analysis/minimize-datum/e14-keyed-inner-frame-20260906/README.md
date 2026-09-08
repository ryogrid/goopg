# E-14 prerequisite — keyed inner spill frames: TPC-H values gate

```
label: E14-keyed-inner-frame | date: 2026-09-06
commit under test: 10b34c633 (+ a comment-only follow-up)
suite: TPC-H SF=1, all 24 labels | regime: server defaults, S-cold
GOGC/GOMEMLIMIT: 100/12GiB | cgroup scope goopg-e14 20G/24G/0
fresh capped server, private binary (NOT tmp/goopg-bench-bin — that path
is shared with the nightly lane), port 65433 held under
`flock /tmp/goopg-65433.lock` for the whole run, tpch@tpch
oracle: bench/tpch/baseline-digests.txt (VALUE baseline, captured
  2026-09-03 at 8dc298e92)
```

## Result

`tpch-runner -diff bench/tpch/baseline-digests.txt run.log`

```
SUMMARY: 24 MATCH
VERDICT: PASS — every label matched on values, not merely on row count
```

Per-label digests are in `tpch-digest-run.log` (colsig + ordered +
unordered per label, so a correct row count computed from wrong values
cannot pass — the failure class this baseline exists for).

## What this gate does and does not establish

**Does:** the change is value-identical on every TPC-H label, including
the ones that actually spill at the bench `work_mem` and therefore
exercise `loadInnerBatch` — the only code path whose behaviour the change
touches. Q9 (175), Q18 (12), Q21 (402) and Q16 (18310) all matched.

**Does not:** it is a values gate, not a timing gate. The change removes
one key-expression evaluation per RELOADED row and adds 9 bytes per
spilled inner row (int lane) to the file; both are small and neither was
measured here. A timing arm was not taken because the change is inert by
construction (same key, read instead of recomputed) and because the
cluster was borrowed between two other agents' runs.

`elapsed=` figures in the run log are single-sample on a cold server and
are NOT an A/B — do not read them as one.
