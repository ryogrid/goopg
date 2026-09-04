# EX3-02 Cut 2 — live gate (stratum D on the Q9 path)

```
label: EX3-02-cut2-live | date: 2026-09-04
binary: tmp/goopg-ex302c2 (Cut A + Cut 1 + Cut 2)
suite: TPC-H SF=1 | regime: stats=S-cold, serial, work_mem 64MB
GOGC/GOMEMLIMIT: 100/12GiB | cgroup scope ex302c2 20G/24G/0
fresh server per arm | port 65433 tpch@tpch
alloc: pprof heap alloc_space -base before/after (endpoint verified
  owned by the measured server PID)
```

## Unit gate (exact)

| lane | legacy | Cut 2 |
|---|---|---|
| arena header-only | 2.002 allocs/row | **0.005** |
| arena full (hdr+2 payloads) | 4.002 | **0.007** |
| int make-lane | 1.000 | 1.000 (untouched by construction) |

9 new tests (contiguity, probe-after-reset, demote re-key, composite +
pack-miss demotion, F7 panic + diversion, Buf-row heap rule, pool-alias
guard, cells ownership, alloc census); 15/15 DenseBuild green.

## Live gate

| | Cut 1 | Cut 2 |
|---|---|---|
| Q9 serial, fresh server | 17.85 s | 18.61 s (in-band 13.9–20.1, NO win claimed, single samples) |
| Q9 alloc window | 8.58 GB | **5.46 GB** (single-sample; unit census is the hard gate) |
| `retainBuildRowHeap` flat | — | 60.5 MB (heap lane only: Buf/non-arena rows) |
| `packDenseBuildRow` flat | — | 0 (bump allocation inside mmgr, by design) |
| Q9 values hash | `6bbb80a6…` | `6bbb80a6…` identical ✓ |
| TPC-H values | — | **24/24 MATCH** ✓ |
| plan-gate | — | **22/22** ✓ |
| TPC-DS SF0.5 | — | **PASS=95 MISMATCH=0** ✓ |

## Safety case (reviewed in-tree, not just tested)

- F1: `rowHasBuf` pre-check keeps every Buf-carrying row fully
  heap-backed; chunk Datums are pointer-free (Buf==nil, value kinds +
  re-homed arena payloads) → noscan-safe.
- F7: `ArenaID!=0 && Buf!=nil` panics loudly at pack time.
- `len==0` → heap path (no `&mem[0]` on empty slice); 8-byte alignment
  for 48 B Datum (asserted layout); filed ranges never written again
  (probe composes read-only VirtualSlots; demote moves headers).
- Shared path: workers never retain (single leader loop); leader cells
  reclaimed at statement end via the parent chain (explicit shared
  teardown = Cut 3).
