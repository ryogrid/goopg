# EX3-02 Cut 1 — live gate (stratum B on the Q9 path)

```
label: EX3-02-cut1-live | date: 2026-09-04
binary: tmp/goopg-ex302c1 (Cut A + Cut 1)
suite: TPC-H SF=1 | regime: stats=S-cold, serial, work_mem 64MB
GOGC/GOMEMLIMIT: 100/12GiB | cgroup scope ex302c1 20G/24G/0
fresh server per arm | port 65433 tpch@tpch
alloc: pprof heap alloc_space -base before/after (endpoint verified
  owned by the measured server PID)
```

## Unit gate (exact)

| shape | legacy | Cut 1 | predicted |
|---|---|---|---|
| A (Q9-class 7-col) | 5.00 allocs/row | **2.00** | 5→~2 ✓ exact |
| C (Q8-class big-numeric) | 6.00 allocs/row | **2.00** | 6→~2 ✓ exact |
| B (int lane) | 1.00 | 1.00 (untouched by construction) | — |

## Live gate

| | slice-3 (pre) | Cut 1 |
|---|---|---|
| Q9 serial, fresh server | 13.88 s | 17.85 s (inside the observed fresh-server band 13.9–20.1 s — NO win claimed, single samples) |
| Q9 alloc window | 8.52 GB | 8.58 GB (window dominated by scan/decode arenas, not the build path) |
| `retainBuildRow` on-path | — | 61.5 MB flat (headers; payloads via mmgr, by design) |
| `MaterializeArena` delta | — | 220 MB (payloads re-homed, down the legacy path) |
| Q9 values hash | `6bbb80a6…` | `6bbb80a6…` identical ✓ |
| TPC-H values | — | **24/24 MATCH** ✓ |
| plan-gate | — | **22/22** ✓ |
| TPC-DS SF0.5 | — | **PASS=95 MISMATCH=0** ✓ |

## Reviewed deviations

- Degenerate-path hardening: short-src (`len(src) != length`, fires only
  on released/ABA-recycled arenas — unreachable in the synchronous build
  loop) yields bare `Datum{Kind}` instead of legacy's zero-padded copy.
  Fail-closed, never fires live; values gates confirm no divergence.
- `MaterializeArena`/`cloneRowOwned` bit-identical (F3); strata parented
  to the statement context with shared-build adoption (F2/F5); `Buf`
  carriers take the legacy path (F1); big-numeric skips decode/re-encode
  temporaries (this, not just the Perm move, takes C 6→2).
